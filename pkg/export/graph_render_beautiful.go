package export

import (
	"encoding/json"
	"fmt"
	"html"
	"sort"
	"strings"
	"time"
	"unicode"
)

func formatFilterOptionLabel(raw string) string {
	cleaned := strings.NewReplacer("_", " ", "-", " ").Replace(strings.TrimSpace(raw))
	parts := strings.Fields(cleaned)
	if len(parts) == 0 {
		return raw
	}
	for i, part := range parts {
		runes := []rune(part)
		runes[0] = unicode.ToUpper(runes[0])
		parts[i] = string(runes)
	}
	return strings.Join(parts, " ")
}

func buildCustomFilterOptionsHTML(graphDataJSON string) (string, string) {
	var parsed struct {
		Nodes []struct {
			Status string `json:"status"`
			Type   string `json:"type"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal([]byte(graphDataJSON), &parsed); err != nil {
		return "", ""
	}
	builtinStatuses := map[string]bool{
		"": true, "all": true, "actionable": true,
		"open": true, "in_progress": true, "blocked": true, "closed": true,
	}
	builtinTypes := map[string]bool{
		"": true, "all": true,
		"feature": true, "bug": true, "task": true, "epic": true,
	}
	customStatusSet := make(map[string]bool)
	customTypeSet := make(map[string]bool)
	for _, n := range parsed.Nodes {
		st := strings.TrimSpace(n.Status)
		if !builtinStatuses[st] {
			customStatusSet[st] = true
		}
		tp := strings.TrimSpace(n.Type)
		if !builtinTypes[tp] {
			customTypeSet[tp] = true
		}
	}

	var statusKeys []string
	for k := range customStatusSet {
		statusKeys = append(statusKeys, k)
	}
	sort.Strings(statusKeys)

	var typeKeys []string
	for k := range customTypeSet {
		typeKeys = append(typeKeys, k)
	}
	sort.Strings(typeKeys)

	var statusSB, typeSB strings.Builder
	for _, st := range statusKeys {
		statusSB.WriteString("\n                    <option value=\"")
		statusSB.WriteString(html.EscapeString(st))
		statusSB.WriteString("\">")
		statusSB.WriteString(html.EscapeString(formatFilterOptionLabel(st)))
		statusSB.WriteString("</option>")
	}
	for _, tp := range typeKeys {
		typeSB.WriteString("\n                    <option value=\"")
		typeSB.WriteString(html.EscapeString(tp))
		typeSB.WriteString("\">")
		typeSB.WriteString(html.EscapeString(formatFilterOptionLabel(tp)))
		typeSB.WriteString("</option>")
	}
	return statusSB.String(), typeSB.String()
}

// generateUltimateHTML creates the enhanced HTML visualization with all features
func generateUltimateHTML(title, dataHash, graphDataJSON string, nodeCount, edgeCount int, projectName, forceGraphLib, markedLib string) string {
	return generateUltimateHTMLWithTime(title, dataHash, graphDataJSON, nodeCount, edgeCount, projectName, forceGraphLib, markedLib, resolveInteractiveExportTime(time.Time{}))
}

func generateUltimateHTMLWithTime(title, dataHash, graphDataJSON string, nodeCount, edgeCount int, projectName, forceGraphLib, markedLib string, exportTime time.Time) string {
	timestamp := exportTime.Format("2006-01-02 15:04:05")
	safeTitle := html.EscapeString(title)
	safeHash := html.EscapeString(dataHash)
	safeProject := html.EscapeString(projectName)
	rendered := fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <meta http-equiv="Content-Security-Policy" content="default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data: blob:; font-src data:; connect-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'">
    <title>%s | bv Graph</title>
    <link rel="icon" href="data:,">
    <!-- Self-contained by design: no external stylesheets, scripts, or fonts.
         Inter / JetBrains Mono are used when installed locally; the stacks
         below fall back to the system UI and monospace faces. -->
    <style>
        :root {
            --bg: #0f0f1a;
            --bg-secondary: #1a1a2e;
            --bg-tertiary: #16213e;
            --bg-elevated: #252545;
            --bg-glass: rgba(26, 26, 46, 0.85);
            --fg: #e8e8f0;
            --fg-muted: #8888aa;
            --fg-dim: #555577;
            --purple: #a855f7;
            --purple-glow: rgba(168, 85, 247, 0.4);
            --pink: #ec4899;
            --cyan: #22d3ee;
            --green: #22c55e;
            --orange: #f97316;
            --red: #ef4444;
            --yellow: #eab308;
            --gold: #fbbf24;
            --gold-glow: rgba(251, 191, 36, 0.6);
            --shadow: 0 8px 32px rgba(0,0,0,0.4);
            --shadow-glow: 0 0 40px var(--purple-glow);
            --radius: 12px;
            --radius-lg: 16px;
        }
        /* Light mode overrides */
        body.light-mode {
            --bg: #f8f9fc;
            --bg-secondary: #ffffff;
            --bg-tertiary: #f0f2f5;
            --bg-elevated: #e8eaf0;
            --bg-glass: rgba(255, 255, 255, 0.9);
            --fg: #1a1a2e;
            --fg-muted: #555577;
            --fg-dim: #8888aa;
            --shadow: 0 8px 32px rgba(0,0,0,0.1);
        }
        body.light-mode #graph-container {
            background: radial-gradient(ellipse at center, #ffffff 0%%, #f0f2f5 100%%);
        }
        * { box-sizing: border-box; margin: 0; padding: 0; }
        body {
            font-family: 'Inter', -apple-system, BlinkMacSystemFont, sans-serif;
            background: var(--bg);
            color: var(--fg);
            height: 100vh;
            display: flex;
            flex-direction: column;
            overflow: hidden;
            font-size: 14px;
            line-height: 1.5;
        }

        /* Header */
        header {
            background: linear-gradient(135deg, var(--bg-secondary), var(--bg-tertiary));
            padding: 0.75rem 1.5rem;
            display: flex;
            justify-content: space-between;
            align-items: center;
            border-bottom: 1px solid var(--purple);
            z-index: 100;
            box-shadow: var(--shadow);
            backdrop-filter: blur(10px);
        }
        .logo { display: flex; align-items: center; gap: 0.75rem; }
        .logo-icon {
            width: 40px; height: 40px;
            background: linear-gradient(135deg, var(--purple), var(--pink));
            border-radius: var(--radius);
            display: flex; align-items: center; justify-content: center;
            font-family: 'JetBrains Mono', monospace;
            font-weight: 700; font-size: 16px;
            box-shadow: var(--shadow-glow);
        }
        h1 { font-size: 1.25rem; font-weight: 600; letter-spacing: -0.02em; }
        h1 span {
            background: linear-gradient(90deg, var(--purple), var(--pink), var(--cyan));
            -webkit-background-clip: text; -webkit-text-fill-color: transparent;
            background-size: 200%% auto;
            animation: shimmer 3s linear infinite;
        }
        @keyframes shimmer { 0%% { background-position: 0%% center; } 100%% { background-position: 200%% center; } }

        /* Toolbar */
        .toolbar { display: flex; gap: 0.75rem; align-items: center; flex-wrap: wrap; }
        .toolbar-group {
            display: flex; gap: 0.25rem;
            padding: 0.25rem;
            background: var(--bg);
            border-radius: var(--radius);
            border: 1px solid var(--bg-elevated);
        }
        button, select {
            font-family: inherit;
            font-size: 0.8rem;
            padding: 0.5rem 0.875rem;
            border: none;
            border-radius: 8px;
            cursor: pointer;
            transition: all 0.2s ease;
            font-weight: 500;
        }
        button { background: transparent; color: var(--fg-muted); }
        button:hover { background: var(--bg-elevated); color: var(--fg); transform: translateY(-1px); }
        button.active {
            background: linear-gradient(135deg, var(--purple), var(--pink));
            color: white;
            box-shadow: 0 4px 15px var(--purple-glow);
        }
        select {
            background: var(--bg);
            color: var(--fg);
            border: 1px solid var(--bg-elevated);
            padding-right: 2rem;
        }
        select:focus { outline: none; border-color: var(--purple); box-shadow: 0 0 0 3px var(--purple-glow); }

        /* Search */
        .search-container { position: relative; }
        .search-input {
            font-family: inherit;
            font-size: 0.875rem;
            padding: 0.5rem 1rem 0.5rem 2.5rem;
            background: var(--bg);
            color: var(--fg);
            border: 1px solid var(--bg-elevated);
            border-radius: var(--radius);
            width: 280px;
            transition: all 0.2s ease;
        }
        .search-input:focus {
            outline: none;
            border-color: var(--purple);
            box-shadow: 0 0 0 3px var(--purple-glow);
            width: 320px;
        }
        .search-icon {
            position: absolute; left: 0.875rem; top: 50%%; transform: translateY(-50%%);
            color: var(--fg-muted); font-size: 0.875rem;
        }

        /* Search Results Dropdown */
        .search-results {
            position: absolute; top: 100%%; left: 0; right: 0;
            background: var(--bg-glass); backdrop-filter: blur(20px);
            border: 1px solid var(--purple); border-radius: var(--radius);
            margin-top: 0.5rem; max-height: 400px; overflow-y: auto;
            z-index: 1000; display: none; box-shadow: var(--shadow);
        }
        .search-results.visible { display: block; }
        .search-result-item {
            padding: 0.875rem 1rem; cursor: pointer;
            border-bottom: 1px solid var(--bg-elevated);
            transition: all 0.15s ease;
        }
        .search-result-item:hover { background: var(--bg-elevated); }
        .search-result-item:last-child { border-bottom: none; }
        .search-result-id { font-family: 'JetBrains Mono', monospace; color: var(--cyan); font-size: 0.8rem; font-weight: 600; }
        .search-result-title { font-size: 0.875rem; margin-top: 0.25rem; }
        .search-result-preview {
            font-size: 0.75rem; color: var(--fg-muted); margin-top: 0.5rem;
            max-height: 60px; overflow: hidden;
        }
        .search-result-preview p { margin: 0.25rem 0; }

        /* Main */
        main { flex: 1; display: flex; overflow: hidden; position: relative; }

        /* Detail Sidebar (Left - Docked Mode) */
        #detail-sidebar {
            width: 420px;
            background: linear-gradient(180deg, var(--bg-secondary) 0%%, var(--bg) 100%%);
            border-right: 1px solid var(--purple);
            overflow-y: auto; padding: 0;
            display: flex; flex-direction: column;
            transition: width 0.3s ease, opacity 0.3s ease;
        }
        #detail-sidebar.collapsed { width: 0; padding: 0; overflow: hidden; opacity: 0; }
        #detail-sidebar .panel-header {
            display: flex; justify-content: space-between; align-items: center;
            padding: 1rem 1.25rem; background: var(--bg-tertiary);
            border-bottom: 1px solid var(--bg-elevated);
            position: sticky; top: 0; z-index: 5;
        }
        #detail-sidebar .panel-header h3 {
            font-size: 0.9rem; font-weight: 600; color: var(--fg);
            display: flex; align-items: center; gap: 0.5rem;
        }
        #detail-sidebar .panel-header .icon { color: var(--gold); }
        .detach-btn {
            background: var(--bg-elevated); border: 1px solid var(--fg-dim);
            color: var(--fg-muted); padding: 0.375rem 0.75rem; border-radius: 6px;
            cursor: pointer; font-size: 0.75rem; transition: all 0.15s ease;
            display: flex; align-items: center; gap: 0.375rem;
        }
        .detach-btn:hover { background: var(--purple); color: white; border-color: var(--purple); }
        #detail-sidebar .no-selection {
            display: flex; flex-direction: column; align-items: center; justify-content: center;
            height: 300px; color: var(--fg-muted); text-align: center; padding: 2rem;
        }
        #detail-sidebar .no-selection-icon { font-size: 3rem; margin-bottom: 1rem; opacity: 0.5; }
        #detail-sidebar .no-selection-text { font-size: 0.875rem; }
        #detail-sidebar .detail-content { padding: 1.25rem; flex: 1; }

        #graph-wrapper { flex: 1; position: relative; }
        #graph-container {
            position: absolute; top: 0; left: 0; right: 0; bottom: 0;
            background: radial-gradient(ellipse at center, var(--bg-secondary) 0%%, var(--bg) 100%%);
        }

        /* Overlay Stats */
        .overlay-stats {
            position: absolute; top: 1rem; left: 1rem;
            background: var(--bg-glass); backdrop-filter: blur(15px);
            padding: 0.75rem 1rem; border-radius: var(--radius);
            font-size: 0.8rem; color: var(--fg-muted);
            z-index: 10; display: flex; flex-wrap: wrap; gap: 0.65rem 1.25rem;
            max-width: calc(100%% - 5rem);
            border: 1px solid var(--bg-elevated);
            box-shadow: var(--shadow);
        }
        .overlay-stats .stat { display: flex; align-items: center; gap: 0.375rem; }
        .overlay-stats .stat-value { color: var(--cyan); font-weight: 600; font-family: 'JetBrains Mono', monospace; }

        /* Hover Panel - Full Details (Floating Mode) */
        #hover-panel {
            position: absolute; top: 1rem; left: 50%%; transform: translateX(-50%%);
            background: var(--bg-glass); backdrop-filter: blur(20px);
            border: 1px solid var(--gold); border-radius: var(--radius-lg);
            padding: 1.25rem; width: 500px; max-height: 70vh;
            overflow-y: auto; z-index: 50;
            box-shadow: 0 8px 32px rgba(0,0,0,0.5), 0 0 60px var(--gold-glow);
            display: none;
        }
        #hover-panel.visible { display: block; animation: panelIn 0.2s ease-out; }
        #hover-panel.docked { display: none !important; } /* Hidden when docked mode active */
        @keyframes panelIn { from { opacity: 0; transform: translateX(-50%%) translateY(-10px); } }

        /* Docked Panel Content - inside detail-sidebar */
        #docked-panel {
            display: none;
        }
        #docked-panel.visible { display: block; }
        .hover-header { display: flex; justify-content: space-between; align-items: flex-start; margin-bottom: 1rem; }
        .hover-id { font-family: 'JetBrains Mono', monospace; font-size: 1rem; color: var(--cyan); font-weight: 600; }
        .hover-type-badge {
            padding: 0.25rem 0.625rem; border-radius: 6px;
            font-size: 0.7rem; font-weight: 600; text-transform: uppercase; letter-spacing: 0.05em;
        }
        .hover-title { font-size: 1.1rem; font-weight: 600; margin-bottom: 1rem; line-height: 1.4; }
        .hover-badges { display: flex; flex-wrap: wrap; gap: 0.375rem; margin-bottom: 1rem; }
        .hover-section { margin-bottom: 1rem; }
        .hover-section-title {
            font-size: 0.7rem; font-weight: 600; color: var(--purple);
            text-transform: uppercase; letter-spacing: 0.1em; margin-bottom: 0.5rem;
            padding-bottom: 0.25rem; border-bottom: 1px solid var(--bg-elevated);
        }
        .hover-content { font-size: 0.875rem; color: var(--fg); line-height: 1.6; }
        .hover-content p { margin-bottom: 0.5rem; }
        .hover-content ul, .hover-content ol { margin-left: 1.25rem; margin-bottom: 0.5rem; }
        .hover-content code {
            background: var(--bg); padding: 0.125rem 0.375rem; border-radius: 4px;
            font-family: 'JetBrains Mono', monospace; font-size: 0.8rem;
        }
        .hover-content pre {
            background: var(--bg); padding: 0.75rem; border-radius: 8px;
            overflow-x: auto; margin: 0.5rem 0;
        }
        .hover-content pre code { padding: 0; background: none; }
        .hover-meta { display: grid; grid-template-columns: repeat(2, 1fr); gap: 0.5rem; font-size: 0.8rem; }
        .hover-meta-item { display: flex; flex-direction: column; }
        .hover-meta-label { color: var(--fg-muted); font-size: 0.7rem; }
        .hover-meta-value { font-weight: 500; }
        .hover-deps { display: flex; flex-wrap: wrap; gap: 0.375rem; }
        .hover-dep-chip {
            padding: 0.25rem 0.5rem; background: var(--bg);
            border-radius: 6px; font-size: 0.75rem; font-family: 'JetBrains Mono', monospace;
            cursor: pointer; transition: all 0.15s ease;
        }
        .hover-dep-chip:hover { background: var(--purple); color: white; }
        .hover-commits { max-height: 150px; overflow-y: auto; }
        .hover-commit {
            padding: 0.5rem; background: var(--bg); border-radius: 6px;
            margin-bottom: 0.375rem; font-size: 0.75rem;
        }
        .hover-commit-sha { font-family: 'JetBrains Mono', monospace; color: var(--cyan); }
        .hover-commit-msg { margin-top: 0.25rem; color: var(--fg-muted); }
        .hover-close {
            position: absolute; top: 0.75rem; right: 0.75rem;
            background: none; border: none; color: var(--fg-muted);
            cursor: pointer; font-size: 1.25rem; padding: 0.25rem;
        }
        .hover-close:hover { color: var(--fg); }

        /* Sidebar */
        #sidebar {
            width: 340px;
            background: linear-gradient(180deg, var(--bg-secondary) 0%%, var(--bg) 100%%);
            border-left: 1px solid var(--purple);
            overflow-y: auto; padding: 1.25rem;
            display: flex; flex-direction: column; gap: 1rem;
        }
        .stats-grid { display: grid; grid-template-columns: repeat(2, 1fr); gap: 0.625rem; }
        .stat-card {
            background: var(--bg-tertiary); padding: 0.875rem;
            border-radius: var(--radius); text-align: center;
            border: 1px solid var(--bg-elevated);
            transition: all 0.2s ease;
        }
        .stat-card:hover { border-color: var(--purple); transform: translateY(-2px); box-shadow: var(--shadow); }
        .stat-card .stat-value {
            font-size: 1.75rem; font-weight: 700;
            font-family: 'JetBrains Mono', monospace;
            background: linear-gradient(135deg, var(--green), var(--cyan));
            -webkit-background-clip: text; -webkit-text-fill-color: transparent;
        }
        .stat-card .stat-value.warning {
            background: linear-gradient(135deg, var(--orange), var(--red));
            -webkit-background-clip: text; -webkit-text-fill-color: transparent;
        }
        .stat-card .stat-label {
            font-size: 0.7rem; color: var(--fg-muted);
            text-transform: uppercase; letter-spacing: 0.05em; margin-top: 0.25rem;
        }

        /* Panels */
        .panel {
            background: var(--bg-tertiary); border-radius: var(--radius);
            padding: 1rem; border: 1px solid var(--bg-elevated);
        }
        .panel-title {
            font-size: 0.75rem; font-weight: 600; color: var(--purple);
            text-transform: uppercase; letter-spacing: 0.1em; margin-bottom: 0.75rem;
            display: flex; align-items: center; gap: 0.5rem;
        }
        .panel-title::before {
            content: ''; width: 4px; height: 14px;
            background: linear-gradient(180deg, var(--purple), var(--pink));
            border-radius: 2px;
        }

        /* Legend */
        .legend { display: flex; flex-wrap: wrap; gap: 0.5rem; }
        .legend-item {
            display: flex; align-items: center; gap: 0.375rem;
            font-size: 0.75rem; color: var(--fg-muted);
            padding: 0.25rem 0.5rem; background: var(--bg); border-radius: 6px;
        }
        .legend-dot {
            width: 12px; height: 12px; border-radius: 50%%;
            box-shadow: 0 0 8px currentColor;
        }

        /* Triage Panel */
        .triage-item {
            padding: 0.75rem; background: var(--bg); border-radius: 8px;
            margin-bottom: 0.5rem; cursor: pointer; transition: all 0.15s ease;
            border-left: 3px solid var(--purple);
        }
        .triage-item:hover { transform: translateX(4px); border-left-color: var(--gold); }
        .triage-item-header { display: flex; justify-content: space-between; align-items: center; }
        .triage-item-id { font-family: 'JetBrains Mono', monospace; font-size: 0.75rem; color: var(--cyan); font-weight: 600; }
        .triage-item-score { font-size: 0.7rem; color: var(--gold); font-weight: 600; }
        .triage-item-title { font-size: 0.8rem; margin-top: 0.25rem; }
        .triage-item-reason { font-size: 0.7rem; color: var(--fg-muted); margin-top: 0.375rem; }

        /* Badges */
        .badge {
            font-size: 0.65rem; padding: 0.2rem 0.5rem; border-radius: 4px;
            text-transform: uppercase; font-weight: 600; letter-spacing: 0.03em;
            background: #64748b; color: white;
        }
        .badge-open { background: var(--green); color: var(--bg); }
        .badge-in_progress { background: var(--orange); color: var(--bg); }
        .badge-blocked { background: var(--red); color: white; }
        .badge-deferred, .badge-draft, .badge-review, .badge-pinned, .badge-hooked { background: #64748b; color: white; }
        .badge-closed { background: var(--fg-dim); color: var(--bg); }
        .badge-type { background: var(--purple); color: white; }
        .badge-feature { background: linear-gradient(135deg, var(--purple), var(--pink)); color: white; }
        .badge-bug { background: var(--red); color: white; }
        .badge-task { background: var(--cyan); color: var(--bg); }
        .badge-epic { background: linear-gradient(135deg, var(--gold), var(--orange)); color: var(--bg); }
        .badge-articulation { background: linear-gradient(135deg, var(--pink), var(--purple)); color: white; animation: pulse 2s infinite; }
        .badge-critical { background: linear-gradient(135deg, var(--red), var(--orange)); color: white; }
        @keyframes pulse { 0%%, 100%% { opacity: 1; } 50%% { opacity: 0.7; } }

        /* Node Detail */
        #node-detail { display: none; }
        #node-detail.visible { display: block; }
        .detail-header { display: flex; align-items: flex-start; justify-content: space-between; margin-bottom: 0.75rem; }
        .detail-id { font-size: 1rem; font-weight: 700; color: var(--cyan); font-family: 'JetBrains Mono', monospace; }
        .detail-priority {
            padding: 0.25rem 0.625rem; border-radius: 6px;
            font-size: 0.7rem; font-weight: 700; text-transform: uppercase;
        }
        .detail-name { font-size: 0.875rem; color: var(--fg); line-height: 1.5; margin-bottom: 0.625rem; }
        .detail-badges { display: flex; gap: 0.375rem; flex-wrap: wrap; margin-bottom: 0.75rem; }
        .detail-metrics { display: grid; grid-template-columns: repeat(2, 1fr); gap: 0.375rem; font-size: 0.75rem; }
        .metric-item {
            display: flex; justify-content: space-between;
            padding: 0.375rem 0; border-bottom: 1px solid var(--bg-elevated);
        }
        .metric-label { color: var(--fg-muted); }
        .metric-value { color: var(--fg); font-weight: 500; font-family: 'JetBrains Mono', monospace; }
        .metric-value.highlight { color: var(--green); }
        .no-selection { text-align: center; padding: 2rem 1rem; color: var(--fg-muted); font-size: 0.8rem; }
        .no-selection-icon { font-size: 2rem; margin-bottom: 0.625rem; opacity: 0.4; }

        /* Shortcuts */
        .keyboard-hints { font-size: 0.75rem; color: var(--fg-muted); line-height: 1.8; }
        .keyboard-hints kbd {
            display: inline-block; background: var(--bg);
            padding: 0.2rem 0.5rem; border-radius: 4px;
            margin: 0 0.125rem; border: 1px solid var(--bg-elevated);
            font-family: 'JetBrains Mono', monospace; font-size: 0.7rem;
        }

        /* Footer */
        footer {
            background: var(--bg-tertiary); padding: 0.5rem 1.25rem;
            font-size: 0.75rem; color: var(--fg-muted);
            display: flex; justify-content: space-between; align-items: center;
            border-top: 1px solid var(--bg-elevated);
        }
        footer a { color: var(--cyan); text-decoration: none; transition: color 0.15s; }
        footer a:hover { color: var(--purple); }

        /* Toast */
        .toast {
            position: fixed; bottom: 100px; left: 50%%; transform: translateX(-50%%);
            background: var(--bg-glass); backdrop-filter: blur(15px);
            border: 1px solid var(--purple); padding: 0.75rem 1.5rem;
            border-radius: var(--radius); font-size: 0.875rem;
            z-index: 1000; box-shadow: var(--shadow);
            opacity: 0; transition: all 0.3s ease;
        }
        .toast.visible { opacity: 1; transform: translateX(-50%%) translateY(-10px); }

        /* Context Menu */
        .context-menu {
            position: fixed; background: var(--bg-glass); backdrop-filter: blur(20px);
            border: 1px solid var(--purple); border-radius: var(--radius);
            padding: 0.5rem 0; z-index: 1000; min-width: 200px;
            box-shadow: var(--shadow); display: none;
        }
        .context-menu.visible { display: block; animation: menuIn 0.15s ease-out; }
        @keyframes menuIn { from { opacity: 0; transform: scale(0.95); } }
        .context-menu-item {
            padding: 0.625rem 1rem; font-size: 0.8rem; cursor: pointer;
            display: flex; align-items: center; gap: 0.625rem; transition: all 0.1s;
        }
        .context-menu-item:hover { background: var(--bg-elevated); }
        .context-menu-divider { height: 1px; background: var(--bg-elevated); margin: 0.375rem 0; }

        /* Fullscreen Button */
        .fullscreen-btn {
            position: absolute; top: 1rem; right: 1rem;
            background: var(--bg-glass); backdrop-filter: blur(10px);
            border: 1px solid var(--purple); color: var(--fg);
            padding: 0.5rem 0.75rem; border-radius: 8px;
            z-index: 10; cursor: pointer; font-size: 1rem;
            transition: all 0.2s ease;
        }
        .fullscreen-btn:hover { background: var(--bg-elevated); box-shadow: var(--shadow-glow); }

        /* Top Nodes Panel */
        .top-nodes-panel {
            position: absolute; top: 60px; right: 1rem;
            background: var(--bg-glass); backdrop-filter: blur(15px);
            border: 1px solid var(--purple); border-radius: var(--radius);
            padding: 0.75rem; z-index: 10; max-height: 250px;
            overflow-y: auto; width: 220px; display: none;
        }
        .top-nodes-panel.visible { display: block; }
        .top-node-item {
            padding: 0.5rem; font-size: 0.75rem; cursor: pointer;
            border-radius: 6px; display: flex; justify-content: space-between;
            transition: all 0.15s ease;
        }
        .top-node-item:hover { background: var(--bg-elevated); transform: translateX(4px); }
        .top-node-item .rank { color: var(--gold); font-weight: 600; }

        /* Heatmap Legend - Always visible */
        .heatmap-legend {
            position: absolute; bottom: 1rem; right: 1rem;
            background: var(--bg-glass); backdrop-filter: blur(15px);
            border: 1px solid var(--fg-dim); border-radius: var(--radius);
            padding: 0.75rem; z-index: 10;
            transition: all 0.2s ease;
        }
        .heatmap-legend.heatmap-active { border-color: var(--gold); box-shadow: 0 0 20px var(--gold-glow); }
        .heatmap-gradient {
            width: 140px; height: 14px;
            background: linear-gradient(90deg, var(--green), var(--yellow), var(--orange), var(--red));
            border-radius: 4px; margin-bottom: 0.375rem;
        }

        /* Mini-map */
        .minimap {
            position: absolute; bottom: 5rem; right: 1rem;
            width: 160px; height: 120px;
            background: var(--bg-glass); backdrop-filter: blur(15px);
            border: 1px solid var(--fg-dim); border-radius: var(--radius);
            overflow: hidden; z-index: 10;
        }
        .minimap canvas { width: 100%%; height: 100%%; }
        .minimap-viewport {
            position: absolute; border: 2px solid var(--gold);
            background: rgba(251, 191, 36, 0.1); pointer-events: none;
        }

        /* Help Overlay */
        .help-overlay {
            position: fixed; top: 0; left: 0; right: 0; bottom: 0;
            background: rgba(0, 0, 0, 0.85); backdrop-filter: blur(10px);
            z-index: 1000; display: none; align-items: center; justify-content: center;
        }
        .help-overlay.visible { display: flex; }
        .help-content {
            background: var(--bg-secondary); border: 1px solid var(--purple);
            border-radius: var(--radius-lg); padding: 2rem; max-width: 700px;
            max-height: 80vh; overflow-y: auto; box-shadow: var(--shadow-glow);
        }
        .help-title { font-size: 1.5rem; font-weight: 700; margin-bottom: 1.5rem; color: var(--gold); }
        .help-section { margin-bottom: 1.5rem; }
        .help-section-title { font-size: 0.8rem; font-weight: 600; color: var(--purple); text-transform: uppercase; letter-spacing: 0.1em; margin-bottom: 0.75rem; }
        .help-grid { display: grid; grid-template-columns: repeat(2, 1fr); gap: 0.5rem; }
        .help-item { display: flex; align-items: center; gap: 0.75rem; font-size: 0.875rem; }
        .help-key {
            background: var(--bg-elevated); padding: 0.25rem 0.5rem; border-radius: 4px;
            font-family: 'JetBrains Mono', monospace; font-size: 0.75rem; min-width: 2rem; text-align: center;
            border: 1px solid var(--fg-dim);
        }
        .help-close { position: absolute; top: 1rem; right: 1rem; background: none; border: none; color: var(--fg-muted); cursor: pointer; font-size: 1.5rem; }

        /* Recently Viewed */
        .recent-panel {
            position: absolute; top: 60px; left: 1rem;
            background: var(--bg-glass); backdrop-filter: blur(15px);
            border: 1px solid var(--purple); border-radius: var(--radius);
            padding: 0.75rem; z-index: 10; width: 200px; display: none;
        }
        .recent-panel.visible { display: block; }
        .recent-title { font-size: 0.7rem; font-weight: 600; color: var(--fg-muted); text-transform: uppercase; margin-bottom: 0.5rem; }
        .recent-item {
            padding: 0.375rem 0.5rem; font-size: 0.75rem; cursor: pointer;
            border-radius: 4px; transition: all 0.15s ease;
            white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
        }
        .recent-item:hover { background: var(--bg-elevated); }
        .recent-item .recent-id { color: var(--cyan); font-family: 'JetBrains Mono', monospace; }

        /* Path Finder Mode */
        .pathfinder-banner {
            position: absolute; top: 4rem; left: 50%%; transform: translateX(-50%%);
            background: var(--gold); color: #0f0f1a; padding: 0.5rem 1rem;
            border-radius: var(--radius); font-weight: 600; font-size: 0.875rem;
            z-index: 100; display: none; animation: pulse 1.5s infinite;
        }
        .pathfinder-banner.visible { display: block; }
        @keyframes pulse { 0%%, 100%% { opacity: 1; } 50%% { opacity: 0.7; } }

        /* Edge Label Tooltip */
        .edge-tooltip {
            position: absolute; background: var(--bg-elevated); border: 1px solid var(--purple);
            padding: 0.375rem 0.625rem; border-radius: 6px; font-size: 0.75rem;
            pointer-events: none; z-index: 100; display: none; white-space: nowrap;
        }
        .edge-tooltip.visible { display: block; }
        .heatmap-labels { display: flex; justify-content: space-between; font-size: 0.7rem; color: var(--fg-muted); }

        /* Scrollbar */
        ::-webkit-scrollbar { width: 8px; height: 8px; }
        ::-webkit-scrollbar-track { background: var(--bg); }
        ::-webkit-scrollbar-thumb { background: var(--bg-elevated); border-radius: 4px; }
        ::-webkit-scrollbar-thumb:hover { background: var(--purple); }
    </style>
</head>
<body>
    <header>
        <div class="logo">
            <div class="logo-icon">bv</div>
            <h1><span>%s</span> Graph</h1>
        </div>
        <div class="toolbar">
            <div class="search-container">
                <span class="search-icon">🔍</span>
                <input type="text" class="search-input" id="search-input" placeholder="Search beads... (full text)">
                <div class="search-results" id="search-results"></div>
            </div>
            <div class="toolbar-group">
                <select id="view-mode" title="Graph layout mode: Force (physics simulation), DAG (directed acyclic graph), or Radial. Press 1-4 for shortcuts.">
                    <option value="force">Force</option>
                    <option value="td">DAG ↓</option>
                    <option value="lr">DAG →</option>
                    <option value="radialout">Radial</option>
                </select>
            </div>
            <div class="toolbar-group">
                <select id="filter-status" title="Filter nodes by status or readiness. Shows only beads matching the selected filter.">
                    <option value="all">All Status</option>
                    <option value="actionable">Actionable</option>
                    <option value="open">Open</option>
                    <option value="in_progress">In Progress</option>
                    <option value="blocked">Blocked</option>
                    <option value="closed">Closed</option>
                </select>
                <select id="filter-type" title="Filter nodes by type. Shows only beads matching the selected type.">
                    <option value="all">All Types</option>
                    <option value="feature">Feature</option>
                    <option value="bug">Bug</option>
                    <option value="task">Task</option>
                    <option value="epic">Epic</option>
                </select>
            </div>
            <div class="toolbar-group">
                <select id="size-by" title="Control what metric determines node size. Larger nodes have higher values of the selected metric.">
                    <option value="pagerank">Size: PageRank</option>
                    <option value="betweenness">Size: Betweenness</option>
                    <option value="critical">Size: Critical Path</option>
                    <option value="indegree">Size: In-Degree</option>
                    <option value="risk">Size: Risk</option>
                </select>
            </div>
            <div class="toolbar-group">
                <select id="filter-priority" title="Filter nodes by priority level (P0=critical, P4=backlog)">
                    <option value="all">All Priorities</option>
                    <option value="0">P0 Critical</option>
                    <option value="1">P1 High</option>
                    <option value="2">P2 Medium</option>
                    <option value="3">P3 Low</option>
                    <option value="4">P4 Backlog</option>
                </select>
                <select id="filter-label" title="Filter nodes by label">
                    <option value="all">All Labels</option>
                </select>
                <select id="filter-repo" title="Filter nodes by source repository" style="display:none">
                    <option value="all">All Repos</option>
                </select>
            </div>
            <div class="toolbar-group">
                <button id="btn-heatmap" title="Toggle heatmap coloring - shows node importance by color intensity (H)">🔥</button>
                <button id="btn-triage" title="Show/hide triage recommendations panel with prioritized work items (G)">📋</button>
                <button id="btn-cycles" title="Inspect dependency cycles (C)" style="display:none">🔄</button>
                <button id="btn-diff-only" title="Toggle Changed Only (diff overlay)" style="display:none">Δ Changed</button>
                <button id="btn-top" title="Show/hide top nodes panel with highest PageRank nodes (T)">⭐</button>
                <button id="btn-recent" title="Show/hide recently viewed nodes (Y)">🕐</button>
                <button id="btn-path" title="Enter path finder mode - click two nodes to find shortest path (P)">🛤️</button>
                <button id="btn-theme" title="Switch to light mode (L)">☀️</button>
                <button id="btn-help" title="Show keyboard shortcuts and help (?)">❓</button>
                <button id="btn-fit" title="Fit all nodes in view (F)">Fit</button>
                <button id="btn-reset" title="Reset graph to initial state with all filters cleared (R)">Reset</button>
            </div>
        </div>
    </header>
    <main>
        <div id="detail-sidebar">
            <div class="panel-header">
                <h3><span class="icon">✨</span> Bead Details</h3>
                <button class="detach-btn" id="btn-detach" title="Detach panel (D)">
                    <span>⇱</span> Detach
                </button>
            </div>
            <div id="docked-no-selection" class="no-selection">
                <div class="no-selection-icon">🔍</div>
                <div class="no-selection-text">Hover over a node to see details</div>
            </div>
            <div id="docked-panel" class="detail-content">
                <div class="hover-header">
                    <span class="hover-id" id="docked-id">-</span>
                    <span class="hover-type-badge" id="docked-type-badge">-</span>
                </div>
                <div class="hover-title" id="docked-title">-</div>
                <div class="hover-badges" id="docked-badges"></div>
                <div id="docked-description" class="hover-section" style="display:none;">
                    <div class="hover-section-title">Description</div>
                    <div class="hover-content" id="docked-description-content"></div>
                </div>
                <div id="docked-design" class="hover-section" style="display:none;">
                    <div class="hover-section-title">Design</div>
                    <div class="hover-content" id="docked-design-content"></div>
                </div>
                <div id="docked-acceptance" class="hover-section" style="display:none;">
                    <div class="hover-section-title">Acceptance Criteria</div>
                    <div class="hover-content" id="docked-acceptance-content"></div>
                </div>
                <div id="docked-notes" class="hover-section" style="display:none;">
                    <div class="hover-section-title">Notes</div>
                    <div class="hover-content" id="docked-notes-content"></div>
                </div>
                <div class="hover-section">
                    <div class="hover-section-title">Metadata</div>
                    <div class="hover-meta" id="docked-meta"></div>
                </div>
                <div id="docked-root-blockers" class="hover-section" style="display:none;">
                    <div class="hover-section-title" id="docked-root-blockers-title">🎯 Start Here: Root Blockers</div>
                    <div class="hover-deps" id="docked-root-blockers-list"></div>
                </div>
                <div id="docked-blocked-by" class="hover-section" style="display:none;">
                    <div class="hover-section-title">Blocked By</div>
                    <div class="hover-deps" id="docked-blocked-by-list"></div>
                </div>
                <div id="docked-blocks" class="hover-section" style="display:none;">
                    <div class="hover-section-title">Blocks</div>
                    <div class="hover-deps" id="docked-blocks-list"></div>
                </div>
                <div id="docked-commits" class="hover-section" style="display:none;">
                    <div class="hover-section-title">Related Commits</div>
                    <div class="hover-commits" id="docked-commits-list"></div>
                </div>
                <div class="hover-section">
                    <div class="hover-section-title">Graph Metrics</div>
                    <div class="hover-meta" id="docked-metrics"></div>
                </div>
            </div>
        </div>
        <div id="graph-wrapper">
            <div id="graph-container"></div>
            <div class="overlay-stats">
                <div class="stat"><span class="stat-value">%d</span> nodes</div>
                <div class="stat"><span class="stat-value">%d</span> edges</div>
                <div class="stat" id="stat-visible"><span class="stat-value">%d</span> visible</div>
                <div class="stat" id="stat-history" style="display:none"><span class="stat-value" id="stat-history-value"></span></div>
            </div>
            <div class="pathfinder-banner" id="diff-banner" style="bottom:1rem;top:auto;left:1rem;transform:none;animation:none;background:var(--bg-glass);color:var(--fg);border:1px solid #22c55e;display:none;flex-wrap:wrap;max-width:calc(100%% - 12.5rem);gap:0.5rem;align-items:center;font-size:0.75rem">
                <span id="diff-banner-text" style="min-width:0;word-break:break-word">Δ Diff</span>
                <button type="button" id="btn-diff-filter-toggle" style="padding:0.15rem 0.45rem;font-size:0.7rem;background:var(--bg-elevated);color:var(--fg);border:1px solid var(--fg-dim);border-radius:4px;cursor:pointer;flex-shrink:0">Changed only</button>
            </div>
            <button class="fullscreen-btn" id="btn-fullscreen" title="Toggle fullscreen mode for immersive graph viewing (Space)">⛶</button>
            <div class="top-nodes-panel" id="top-nodes-panel"></div>
            <div class="heatmap-legend" id="heatmap-legend">
                <div class="heatmap-gradient"></div>
                <div class="heatmap-labels"><span>Low</span><span id="heatmap-metric">PageRank</span><span>High</span></div>
            </div>
            <div class="minimap" id="minimap">
                <canvas id="minimap-canvas"></canvas>
                <div class="minimap-viewport" id="minimap-viewport"></div>
            </div>
            <div class="recent-panel" id="recent-panel">
                <div class="recent-title">Recently Viewed</div>
                <div id="recent-list"></div>
            </div>
            <div class="pathfinder-banner" id="pathfinder-banner">🔗 Path Finder: Click destination node (Esc to cancel)</div>
            <div class="pathfinder-banner" id="cycle-banner" style="top:3.5rem;border-color:#ef4444;display:none;gap:0.5rem;align-items:center">
                <span id="cycle-banner-text">🔄 Cycle</span>
                <button type="button" id="btn-cycle-preview-cut" style="display:none;padding:0.15rem 0.45rem;font-size:0.75rem;background:#d97706;color:#fff;border:none;border-radius:4px;cursor:pointer">Preview Cut</button>
                <button type="button" id="btn-cycle-prev" style="padding:0.15rem 0.45rem;font-size:0.75rem">◀</button>
                <button type="button" id="btn-cycle-next" style="padding:0.15rem 0.45rem;font-size:0.75rem">▶</button>
                <button type="button" id="btn-cycle-close" style="padding:0.15rem 0.45rem;font-size:0.75rem">×</button>
            </div>
            <div class="pathfinder-banner" id="whatif-banner" style="top:3.5rem;border-color:#10b981;display:none;gap:0.5rem;align-items:center">
                <span id="whatif-banner-text">⚡ What-If</span>
                <button type="button" id="btn-whatif-close" style="padding:0.15rem 0.45rem;font-size:0.75rem">Reset</button>
            </div>
            <div class="pathfinder-banner" id="checkdep-banner" style="top:3.5rem;border-color:#6366f1;display:none;gap:0.5rem;align-items:center">
                <span id="checkdep-banner-text">🧪 Check Add Dep</span>
                <button type="button" id="btn-checkdep-copy" style="display:none;padding:0.15rem 0.45rem;font-size:0.75rem">Copy cmd</button>
                <button type="button" id="btn-checkdep-close" style="padding:0.15rem 0.45rem;font-size:0.75rem">Reset</button>
            </div>
            <div class="edge-tooltip" id="edge-tooltip"></div>
            <div id="hover-panel">
                <button class="hover-close" id="hover-close">×</button>
                <div class="hover-header">
                    <span class="hover-id" id="hover-id">-</span>
                    <span class="hover-type-badge" id="hover-type-badge">-</span>
                </div>
                <div class="hover-title" id="hover-title">-</div>
                <div class="hover-badges" id="hover-badges"></div>
                <div id="hover-description" class="hover-section" style="display:none;">
                    <div class="hover-section-title">Description</div>
                    <div class="hover-content" id="hover-description-content"></div>
                </div>
                <div id="hover-design" class="hover-section" style="display:none;">
                    <div class="hover-section-title">Design</div>
                    <div class="hover-content" id="hover-design-content"></div>
                </div>
                <div id="hover-acceptance" class="hover-section" style="display:none;">
                    <div class="hover-section-title">Acceptance Criteria</div>
                    <div class="hover-content" id="hover-acceptance-content"></div>
                </div>
                <div id="hover-notes" class="hover-section" style="display:none;">
                    <div class="hover-section-title">Notes</div>
                    <div class="hover-content" id="hover-notes-content"></div>
                </div>
                <div class="hover-section">
                    <div class="hover-section-title">Metadata</div>
                    <div class="hover-meta" id="hover-meta"></div>
                </div>
                <div id="hover-root-blockers" class="hover-section" style="display:none;">
                    <div class="hover-section-title" id="hover-root-blockers-title">🎯 Start Here: Root Blockers</div>
                    <div class="hover-deps" id="hover-root-blockers-list"></div>
                </div>
                <div id="hover-blocked-by" class="hover-section" style="display:none;">
                    <div class="hover-section-title">Blocked By</div>
                    <div class="hover-deps" id="hover-blocked-by-list"></div>
                </div>
                <div id="hover-blocks" class="hover-section" style="display:none;">
                    <div class="hover-section-title">Blocks</div>
                    <div class="hover-deps" id="hover-blocks-list"></div>
                </div>
                <div id="hover-commits" class="hover-section" style="display:none;">
                    <div class="hover-section-title">Related Commits</div>
                    <div class="hover-commits" id="hover-commits-list"></div>
                </div>
                <div class="hover-section">
                    <div class="hover-section-title">Graph Metrics</div>
                    <div class="hover-meta" id="hover-metrics"></div>
                </div>
            </div>
        </div>
        <div id="sidebar">
            <div class="stats-grid">
                <div class="stat-card"><div class="stat-value" id="stat-nodes">%d</div><div class="stat-label">Nodes</div></div>
                <div class="stat-card"><div class="stat-value" id="stat-edges">%d</div><div class="stat-label">Edges</div></div>
                <div class="stat-card"><div class="stat-value" id="stat-actionable">-</div><div class="stat-label">Actionable</div></div>
                <div class="stat-card"><div class="stat-value warning" id="stat-blocked">-</div><div class="stat-label">Blocked</div></div>
                <div class="stat-card"><div class="stat-value" id="stat-critical">-</div><div class="stat-label">Critical</div></div>
                <div class="stat-card"><div class="stat-value warning" id="stat-articulation">-</div><div class="stat-label">Cut Pts</div></div>
            </div>
            <div class="panel" id="triage-panel" style="display:none;">
                <div class="panel-title">Triage Insights</div>
                <div id="triage-tabs" style="display:flex;gap:0.25rem;flex-wrap:wrap;margin-bottom:0.5rem">
                    <button type="button" class="triage-tab-btn active" data-tab="recommendations" style="padding:0.2rem 0.45rem;font-size:0.7rem">Top</button>
                    <button type="button" class="triage-tab-btn" data-tab="quick_wins" style="padding:0.2rem 0.45rem;font-size:0.7rem">Quick Wins</button>
                    <button type="button" class="triage-tab-btn" data-tab="blockers" style="padding:0.2rem 0.45rem;font-size:0.7rem">Blockers</button>
                    <button type="button" class="triage-tab-btn" data-tab="alerts" style="padding:0.2rem 0.45rem;font-size:0.7rem">Alerts</button>
                    <button type="button" class="triage-tab-btn" data-tab="high_risk" style="padding:0.2rem 0.45rem;font-size:0.7rem">High Risk</button>
                    <button type="button" class="triage-tab-btn" data-tab="structural" style="padding:0.2rem 0.45rem;font-size:0.7rem">Structural</button>
                </div>
                <div id="triage-cross-repo-summary" style="display:none;margin-bottom:0.5rem;padding:0.4rem 0.5rem;border-radius:6px;background:rgba(99,102,241,0.14);border:1px solid rgba(129,140,248,0.35);font-size:0.7rem;color:#c7d2fe;line-height:1.35"></div>
                <div id="triage-list"></div>
            </div>
            <div class="panel">
                <div class="panel-title">Status Legend</div>
                <div class="legend">
                    <div class="legend-item"><div class="legend-dot" style="background:#22c55e;color:#22c55e"></div>Open</div>
                    <div class="legend-item"><div class="legend-dot" style="background:#f97316;color:#f97316"></div>In Progress</div>
                    <div class="legend-item"><div class="legend-dot" style="background:#ef4444;color:#ef4444"></div>Blocked</div>
                    <div class="legend-item"><div class="legend-dot" style="background:#94a3b8;color:#94a3b8"></div>Deferred / Custom</div>
                    <div class="legend-item"><div class="legend-dot" style="background:#555577;color:#555577"></div>Closed</div>
                </div>
            </div>
            <div class="panel">
                <div class="panel-title">Type Shapes</div>
                <div class="legend">
                    <div class="legend-item"><span style="font-size:1rem">●</span> Feature</div>
                    <div class="legend-item"><span style="font-size:1rem">▲</span> Bug</div>
                    <div class="legend-item"><span style="font-size:1rem">■</span> Task</div>
                    <div class="legend-item"><span style="font-size:1rem">◆</span> Epic</div>
                </div>
            </div>
            <div class="panel">
                <div class="panel-title">Selected Node</div>
                <div id="node-detail">
                    <div class="detail-header">
                        <div class="detail-id" id="detail-id">-</div>
                        <div class="detail-priority" id="detail-priority">P2</div>
                    </div>
                    <div class="detail-name" id="detail-name">-</div>
                    <div class="detail-badges" id="detail-badges"></div>
                    <div style="margin:0.5rem 0">
                        <button type="button" id="btn-detail-whatif" style="width:100%%;padding:0.35rem 0.5rem;font-size:0.75rem;display:none">⚡ Simulate Close (What-If)</button>
                    </div>
                    <div id="detail-blockers-wrap" style="margin:0.5rem 0;display:none">
                        <div class="metric-label" style="margin-bottom:0.25rem">Direct Blockers (✂ preflight cut)</div>
                        <div id="detail-blockers-list" style="display:flex;flex-wrap:wrap;gap:0.25rem"></div>
                    </div>
                    <div class="detail-metrics">
                        <div class="metric-item"><span class="metric-label">PageRank</span><span class="metric-value" id="m-pagerank">-</span></div>
                        <div class="metric-item"><span class="metric-label">Rank</span><span class="metric-value" id="m-prrank">-</span></div>
                        <div class="metric-item"><span class="metric-label">Betweenness</span><span class="metric-value" id="m-between">-</span></div>
                        <div class="metric-item"><span class="metric-label">BW Rank</span><span class="metric-value" id="m-bwrank">-</span></div>
                        <div class="metric-item"><span class="metric-label">Critical</span><span class="metric-value" id="m-critical">-</span></div>
                        <div class="metric-item"><span class="metric-label">Slack</span><span class="metric-value" id="m-slack">-</span></div>
                        <div class="metric-item"><span class="metric-label">In-Deg</span><span class="metric-value" id="m-indeg">-</span></div>
                        <div class="metric-item"><span class="metric-label">Out-Deg</span><span class="metric-value" id="m-outdeg">-</span></div>
                    </div>
                </div>
                <div class="no-selection" id="no-selection">
                    <div class="no-selection-icon">🔍</div>
                    Click a node to see details<br>
                    <small>or hover for full info</small>
                </div>
            </div>
            <div class="panel">
                <div class="panel-title">Shortcuts</div>
                <div class="keyboard-hints">
                    <kbd>F</kbd> Fit · <kbd>R</kbd> Reset · <kbd>Space</kbd> Fullscreen<br>
                    <kbd>Esc</kbd> Clear · <kbd>1-4</kbd> View modes<br>
                    <kbd>H</kbd> Heatmap · <kbd>T</kbd> Top · <kbd>G</kbd> Triage<br>
                    <kbd>Shift+Click</kbd> Batch What-If
                </div>
            </div>
        </div>
    </main>
    <footer>
        <div>Generated %s | Hash: %s</div>
        <div>Project: %s | <a href="https://github.com/Dicklesworthstone/beads_viewer">bv</a></div>
    </footer>
    <div class="toast" id="toast"></div>
    <div class="context-menu" id="context-menu">
        <div class="context-menu-item" id="ctx-focus">🎯 Focus on this node</div>
        <div class="context-menu-item" id="ctx-details">📄 Show full details</div>
        <div class="context-menu-item" id="ctx-deps">📥 Show dependencies</div>
        <div class="context-menu-item" id="ctx-dependents">📤 Show dependents</div>
        <div class="context-menu-item" id="ctx-connected">✨ Highlight connected</div>
        <div class="context-menu-item" id="ctx-whatif">⚡ What-If: simulate close</div>
        <div class="context-menu-item" id="ctx-whatif-batch">⚡ Toggle in Batch What-If (Shift+Click)</div>
        <div class="context-menu-item" id="ctx-checkdep">🧪 Check Add Dep → …</div>
        <div class="context-menu-divider"></div>
        <div class="context-menu-item" id="ctx-path">🛤️ Find path to...</div>
        <div class="context-menu-item" id="ctx-copy">📋 Copy ID</div>
        <div class="context-menu-item" id="ctx-copy-description">📝 Copy description</div>
    </div>
    <div class="help-overlay" id="help-overlay">
        <div class="help-content">
            <button class="help-close" id="help-close">×</button>
            <div class="help-title">⌨️ Keyboard Shortcuts</div>
            <div class="help-section">
                <div class="help-section-title">Navigation</div>
                <div class="help-grid">
                    <div class="help-item"><span class="help-key">F</span> Fit all nodes in view</div>
                    <div class="help-item"><span class="help-key">R</span> Reset to initial state</div>
                    <div class="help-item"><span class="help-key">Space</span> Toggle fullscreen</div>
                    <div class="help-item"><span class="help-key">Esc</span> Clear selection</div>
                </div>
            </div>
            <div class="help-section">
                <div class="help-section-title">Views & Panels</div>
                <div class="help-grid">
                    <div class="help-item"><span class="help-key">D</span> Dock/detach detail panel</div>
                    <div class="help-item"><span class="help-key">L</span> Toggle light/dark mode</div>
                    <div class="help-item"><span class="help-key">H</span> Toggle heatmap coloring</div>
                    <div class="help-item"><span class="help-key">T</span> Show top nodes panel</div>
                    <div class="help-item"><span class="help-key">G</span> Show triage panel</div>
                    <div class="help-item"><span class="help-key">Y</span> Show recently viewed</div>
                    <div class="help-item"><span class="help-key">P</span> Enter path finder mode</div>
                    <div class="help-item"><span class="help-key">?</span> Show this help</div>
                </div>
            </div>
            <div class="help-section">
                <div class="help-section-title">Layout Modes</div>
                <div class="help-grid">
                    <div class="help-item"><span class="help-key">1</span> Force-directed layout</div>
                    <div class="help-item"><span class="help-key">2</span> DAG top-down</div>
                    <div class="help-item"><span class="help-key">3</span> DAG left-right</div>
                    <div class="help-item"><span class="help-key">4</span> Radial layout</div>
                </div>
            </div>
            <div class="help-section">
                <div class="help-section-title">Mouse</div>
                <div class="help-grid">
                    <div class="help-item"><span class="help-key">Hover</span> Show node details</div>
                    <div class="help-item"><span class="help-key">Click</span> Select node</div>
                    <div class="help-item"><span class="help-key">Shift+Click</span> Toggle node in Batch What-If</div>
                    <div class="help-item"><span class="help-key">Right-click</span> Context menu</div>
                    <div class="help-item"><span class="help-key">Scroll</span> Zoom in/out</div>
                    <div class="help-item"><span class="help-key">Drag</span> Pan the view</div>
                </div>
            </div>
        </div>
    </div>
    <script>%s</script>
    <script>%s</script>
    <script>
const DATA = %s;
const STATUS_COLORS = { open: '#22c55e', in_progress: '#f97316', blocked: '#ef4444', deferred: '#94a3b8', draft: '#94a3b8', review: '#94a3b8', pinned: '#94a3b8', hooked: '#94a3b8', closed: '#555577', tombstone: '#555577' };
const PRIORITY_COLORS = ['#ef4444', '#f97316', '#eab308', '#22c55e', '#555577'];
const TYPE_COLORS = { feature: '#a855f7', bug: '#ef4444', task: '#22d3ee', epic: '#fbbf24', chore: '#94a3b8' };

// Configure marked for safe HTML rendering
marked.setOptions({ breaks: true, gfm: true });

function escapeHTML(value) {
    return String(value ?? '').replace(/[&<>"']/g, ch => ({
        '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'
    }[ch]));
}

function escapeRegExp(value) {
    return String(value ?? '').replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

function safeClassToken(value) {
    return String(value || '').toLowerCase().replace(/[^a-z0-9_-]+/g, '-');
}

const XHTML_NS = 'http://www.w3.org/1999/xhtml';
const ALLOWED_MARKDOWN_TAGS = new Set([
    'p', 'br', 'strong', 'b', 'em', 'i', 'del', 's', 'code', 'pre',
    'blockquote', 'ul', 'ol', 'li', 'h1', 'h2', 'h3', 'h4', 'h5', 'h6',
    'hr', 'table', 'thead', 'tbody', 'tr', 'th', 'td', 'a', 'span'
]);
const DROP_SUBTREE_TAGS = new Set([
    'script', 'style', 'template', 'iframe', 'object', 'embed', 'form',
    'input', 'button', 'textarea', 'select', 'option', 'meta', 'link',
    'base', 'title', 'noscript', 'noembed', 'noframes', 'xmp', 'svg', 'math'
]);

function isSafeRenderedURL(value) {
    const compact = String(value ?? '').replace(/[\u0000-\u0020]+/g, '');
    if (compact === '') return false;
    const lower = compact.toLowerCase();
    if (compact.startsWith('#')) return true;
    if (
        compact.startsWith('//') ||
        compact.startsWith('\\') ||
        compact.startsWith('/\\') ||
        compact.includes('\\') ||
        lower.startsWith('/%%5c') ||
        lower.startsWith('%%5c')
    ) {
        return false;
    }
    if (compact.startsWith('/') || compact.startsWith('./') || compact.startsWith('../')) {
        return true;
    }
    if (lower.startsWith('http://') || lower.startsWith('https://') || lower.startsWith('mailto:')) {
        try {
            const parsed = new URL(compact);
            return parsed.protocol === 'http:' || parsed.protocol === 'https:' || parsed.protocol === 'mailto:';
        } catch {
            return false;
        }
    }
    return false;
}

function sanitizeHTMLFragment(root) {
    const commentWalker = document.createTreeWalker(root, NodeFilter.SHOW_COMMENT);
    const comments = [];
    while (commentWalker.nextNode()) comments.push(commentWalker.currentNode);
    comments.forEach(c => c.remove());

    const cleanChildren = (parent) => {
        for (const child of Array.from(parent.childNodes)) {
            if (child.nodeType === Node.TEXT_NODE) {
                continue;
            }
            if (child.nodeType !== Node.ELEMENT_NODE) {
                child.remove();
                continue;
            }
            const el = child;
            const tag = (el.localName || '').toLowerCase();
            if (el.namespaceURI !== XHTML_NS || DROP_SUBTREE_TAGS.has(tag)) {
                el.remove();
                continue;
            }
            if (!ALLOWED_MARKDOWN_TAGS.has(tag)) {
                cleanChildren(el);
                el.replaceWith(...Array.from(el.childNodes));
                continue;
            }
            const rawHref = tag === 'a' ? el.getAttribute('href') : null;
            const rawTitle = tag === 'a' ? el.getAttribute('title') : null;
            const rawAlign = (tag === 'th' || tag === 'td') ? el.getAttribute('align') : null;
            const rawClass = tag === 'code' ? el.getAttribute('class') : null;
            for (const attr of Array.from(el.attributes)) {
                el.removeAttributeNode(attr);
            }
            if (tag === 'a') {
                if (rawHref !== null && isSafeRenderedURL(rawHref)) {
                    el.setAttribute('href', String(rawHref).replace(/[\u0000-\u0020]+/g, ''));
                    el.setAttribute('rel', 'noopener noreferrer');
                    el.setAttribute('target', '_blank');
                }
                if (rawTitle !== null && rawTitle !== '') {
                    el.setAttribute('title', rawTitle);
                }
            } else if (tag === 'th' || tag === 'td') {
                const align = String(rawAlign || '').toLowerCase();
                if (align === 'left' || align === 'center' || align === 'right') {
                    el.setAttribute('align', align);
                }
            } else if (tag === 'code') {
                const cls = String(rawClass || '').trim();
                if (/^language-[a-z0-9_-]+$/i.test(cls)) {
                    el.setAttribute('class', cls);
                }
            }
            cleanChildren(el);
        }
    };
    cleanChildren(root);
    return root;
}

function renderSafeMarkdownFragment(value) {
    const raw = String(value ?? '');
    const frag = document.createDocumentFragment();
    if (typeof marked !== 'undefined' && marked && typeof marked.parse === 'function') {
        const template = document.createElement('template');
        template.innerHTML = marked.parse(raw);
        sanitizeHTMLFragment(template.content);
        frag.appendChild(template.content);
    } else {
        const pre = document.createElement('pre');
        pre.textContent = raw;
        frag.appendChild(pre);
    }
    return frag;
}

function highlightMatches(text, query) {
    const source = String(text ?? '');
    const q = String(query ?? '');
    if (!q) return escapeHTML(source);
    const re = new RegExp(escapeRegExp(q), 'gi');
    let out = '', last = 0;
    source.replace(re, (match, offset) => {
        out += escapeHTML(source.slice(last, offset)) + '<mark>' + escapeHTML(match) + '</mark>';
        last = offset + match.length;
        return match;
    });
    return out + escapeHTML(source.slice(last));
}

// Stats calculation (count only active blocking dependencies on non-closed, non-tombstone issues)
const nodeById = {};
DATA.nodes.forEach(n => { nodeById[n.id] = n; });
function isBlockingLinkType(type) {
    return !type || type === 'blocks' || type === 'conditional-blocks' || type === 'waits-for';
}
function isActiveNodeStatus(status) {
    return status !== 'closed' && status !== 'tombstone';
}
let actionable = 0, blocked = 0, onCriticalPath = 0, articulationCount = 0;
const blockerSetByNode = {};
DATA.nodes.forEach(n => {
    const set = new Set();
    (n.blocked_by || []).forEach(depId => {
        const targetNode = nodeById[depId];
        if (!targetNode || isActiveNodeStatus(targetNode.status)) {
            set.add(depId);
        }
    });
    blockerSetByNode[n.id] = set;
});
DATA.links.forEach(l => {
    const src = typeof l.source === 'object' ? l.source.id : l.source;
    const tgt = typeof l.target === 'object' ? l.target.id : l.target;
    const targetNode = nodeById[tgt];
    if (isBlockingLinkType(l.type) && (!targetNode || isActiveNodeStatus(targetNode.status))) {
        if (!blockerSetByNode[src]) blockerSetByNode[src] = new Set();
        blockerSetByNode[src].add(tgt);
    }
});
const EXPORT_NOW_MS = typeof DATA.exported_at_ms === 'number' ? DATA.exported_at_ms : Date.now();
function isNodeDeferred(n) {
    if (!n) return false;
    if (n.status === 'deferred' || Boolean(n.is_deferred)) return true;
    if (n.defer_until) {
        const parsed = Date.parse(n.defer_until);
        if (!Number.isNaN(parsed) && parsed > EXPORT_NOW_MS) return true;
    }
    return false;
}
function isNodeWithheldFromReady(n) {
    if (!n) return false;
    const st = String(n.status || '').toLowerCase();
    return Boolean(n.is_template) || Boolean(n.pinned) || Boolean(n.ephemeral) || st === 'draft' || st === 'pinned' || st === 'hooked' || isNodeDeferred(n);
}
function isNodeBlockedByDeps(n) {
    if (!n || !isActiveNodeStatus(n.status)) return false;
    if (typeof n.dependency_state === 'string' && n.dependency_state !== '') {
        return n.dependency_state !== 'satisfied';
    }
    return (n.blockerCount || 0) > 0;
}
function isNodeActionable(n) {
    if (!n) return false;
    if (typeof n.is_actionable === 'boolean') {
        return n.is_actionable;
    }
    return (n.status === 'open' || n.status === 'in_progress') && !isNodeWithheldFromReady(n) && !isNodeBlockedByDeps(n);
}
function isNodeBlocked(n) {
    if (!n) return false;
    return n.status === 'blocked' || isNodeBlockedByDeps(n);
}

DATA.nodes.forEach(n => {
    n.blockerCount = isActiveNodeStatus(n.status)
        ? (typeof n.blocker_count === 'number' ? n.blocker_count : (blockerSetByNode[n.id] ? blockerSetByNode[n.id].size : 0))
        : 0;
    if (isNodeActionable(n)) actionable++;
    if (isNodeBlocked(n)) blocked++;
    if (n.slack_available !== false && isActiveNodeStatus(n.status) && n.slack === 0) onCriticalPath++;
    if (isActiveNodeStatus(n.status) && n.is_articulation) articulationCount++;
});
document.getElementById('stat-actionable').textContent = actionable;
document.getElementById('stat-blocked').textContent = blocked;
document.getElementById('stat-critical').textContent = onCriticalPath;
document.getElementById('stat-articulation').textContent = articulationCount;

// Max values for sizing
const maxPR = Math.max(...DATA.nodes.map(n => n.pagerank || 0), 0.001);
const maxBW = Math.max(...DATA.nodes.map(n => n.betweenness || 0), 0.001);
const maxCP = Math.max(...DATA.nodes.map(n => n.critical_path || 0), 1);
const maxInDeg = Math.max(...DATA.nodes.map(n => n.in_degree || 0), 1);
const maxRisk = Math.max(...DATA.nodes.map(n => n.risk_score || 0), 0.001);

let sizeMetric = 'pagerank', heatmapMode = false, hoveredNode = null, highlightedNodes = new Set(), activePathNodes = null, isDarkMode = true;
const recentlyViewed = [];
const MAX_RECENT = 8;

function getNodeSize(n) {
    const base = 5, scale = 16;
    switch(sizeMetric) {
        case 'pagerank': return base + ((n.pagerank || 0) / maxPR) * scale;
        case 'betweenness': return base + ((n.betweenness || 0) / maxBW) * scale;
        case 'critical': return base + ((n.critical_path || 0) / maxCP) * scale;
        case 'indegree': return base + ((n.in_degree || 0) / maxInDeg) * scale;
        case 'risk': return base + ((n.risk_score || 0) / maxRisk) * scale;
        default: return base + ((n.pagerank || 0) / maxPR) * scale;
    }
}

function getHeatmapColor(n, alpha) {
    let val = 0, max = 1;
    switch(sizeMetric) {
        case 'pagerank': val = n.pagerank || 0; max = maxPR; break;
        case 'betweenness': val = n.betweenness || 0; max = maxBW; break;
        case 'critical': val = n.critical_path || 0; max = maxCP; break;
        case 'indegree': val = n.in_degree || 0; max = maxInDeg; break;
        case 'risk': val = n.risk_score || 0; max = maxRisk; break;
    }
    const ratio = val / max;
    const hue = 120 - ratio * 120; // Green to red
    if (alpha !== undefined) {
        return 'hsla(' + hue + ', 80%%, 50%%, ' + alpha + ')';
    }
    return 'hsl(' + hue + ', 80%%, 50%%)';
}

function getNodeStatusColor(n) {
    if (!isActiveNodeStatus(n.status)) {
        return STATUS_COLORS[n.status] || STATUS_COLORS.closed;
    }
    if (isNodeBlocked(n)) {
        return STATUS_COLORS.blocked;
    }
    if (isNodeActionable(n)) {
        return n.status === 'in_progress' ? STATUS_COLORS.in_progress : STATUS_COLORS.open;
    }
    if (isNodeWithheldFromReady(n)) {
        return '#94a3b8';
    }
    if (STATUS_COLORS[n.status]) {
        return STATUS_COLORS[n.status];
    }
    return '#94a3b8';
}

function computeNodeBaseColor(n) {
    return heatmapMode ? getHeatmapColor(n) : getNodeStatusColor(n);
}

function computeNodeColor(n) {
    const dimmed = highlightedNodes.size > 0 && !highlightedNodes.has(n.id);
    if (heatmapMode) {
        return dimmed ? getHeatmapColor(n, 0.15) : getHeatmapColor(n);
    }
    const col = getNodeStatusColor(n);
    return dimmed ? col + '20' : col;
}

// Get connected subgraph (for golden glow highlight)
function getConnectedNodes(nodeId, depth = 2) {
    const connected = new Set([nodeId]);
    const queue = [{id: nodeId, d: 0}];
    while (queue.length > 0) {
        const {id, d} = queue.shift();
        if (d >= depth) continue;
        DATA.links.forEach(l => {
            const src = typeof l.source === 'object' ? l.source.id : l.source;
            const tgt = typeof l.target === 'object' ? l.target.id : l.target;
            if (src === id && !connected.has(tgt)) { connected.add(tgt); queue.push({id: tgt, d: d+1}); }
            if (tgt === id && !connected.has(src)) { connected.add(src); queue.push({id: src, d: d+1}); }
        });
    }
    return connected;
}

const container = document.getElementById('graph-container');
function lookupStandaloneNode(id) {
    if (typeof Graph !== 'undefined' && Graph) {
        const liveNodes = Graph.graphData()?.nodes;
        if (Array.isArray(liveNodes)) {
            const live = liveNodes.find(n => n.id === id);
            if (live) return live;
        }
    }
    if (Array.isArray(DATA?.nodes)) {
        const fromData = DATA.nodes.find(n => n.id === id);
        if (fromData) return fromData;
    }
    return nodeById[id] || null;
}
function buildGraphLinksForMode(blockingOnly) {
    return DATA.links
        .filter(l => {
            if (!blockingOnly) return true;
            if (!isBlockingLinkType(l.type)) return false;
            const s = typeof l.source === 'object' ? l.source.id : l.source;
            const t = typeof l.target === 'object' ? l.target.id : l.target;
            if (!s || !t || s === t) return false;
            const srcNode = lookupStandaloneNode(s);
            const tgtNode = lookupStandaloneNode(t);
            if ((srcNode && !isActiveNodeStatus(srcNode.status)) || (tgtNode && !isActiveNodeStatus(tgtNode.status))) {
                return false;
            }
            return true;
        })
        .map(l => ({
            ...l,
            source: typeof l.source === 'object' ? l.source.id : l.source,
            target: typeof l.target === 'object' ? l.target.id : l.target,
        }));
}
function setGraphLinksMode(blockingOnly) {
    if (typeof Graph === 'undefined' || !Graph) return;
    const cur = Graph.graphData();
    const nodes = Array.isArray(cur?.nodes) && cur.nodes.length > 0 ? cur.nodes : JSON.parse(JSON.stringify(DATA.nodes));
    Graph.graphData({ nodes, links: buildGraphLinksForMode(blockingOnly) });
}
let dagErrorTriggered = false;
function handleDagCycleError() {
    dagErrorTriggered = true;
    const sel = document.getElementById('view-mode');
    if (sel) sel.value = 'force';
    try { Graph.dagMode(null); } catch (_) {}
    try { setGraphLinksMode(false); } catch (_) {}
    localStorage.removeItem('bv-graph-layout');
    if (Array.isArray(DATA.cycles) && DATA.cycles.length > 0) {
        showCycleInspector(0);
        showToast('Cannot apply DAG layout: dependency cycle detected (' + DATA.cycles.length + ' highlighted)');
    } else {
        showToast('Cannot apply DAG layout: dependency cycle detected');
    }
}
function applyGraphDagMode(mode) {
    dagErrorTriggered = false;
    const sel = document.getElementById('view-mode');
    if (!mode || mode === 'force') {
        if (sel) sel.value = 'force';
        Graph.dagMode(null);
        setGraphLinksMode(false);
        localStorage.setItem('bv-graph-layout', 'force');
        return true;
    }
    if (sel) sel.value = mode;
    setGraphLinksMode(true);
    try {
        Graph.dagMode(mode);
    } catch (err) {
        handleDagCycleError();
        return false;
    }
    if (dagErrorTriggered) return false;
    localStorage.setItem('bv-graph-layout', mode);
    return true;
}

function computeLinkColor(l) {
    const src = typeof l.source === 'object' ? l.source.id : l.source;
    const tgt = typeof l.target === 'object' ? l.target.id : l.target;
    if (highlightedNodes.size > 0) {
        if (highlightedNodes.has(src) && highlightedNodes.has(tgt)) return isDarkMode ? '#fbbf24aa' : '#d97706cc';
        return isDarkMode ? '#44475a15' : '#94a3b833';
    }
    if (l.critical) return isDarkMode ? '#ec489980' : '#db277799';
    return isDarkMode ? '#44475a40' : '#64748b80';
}

function computeLinkArrowColor(l) {
    const src = typeof l.source === 'object' ? l.source.id : l.source;
    const tgt = typeof l.target === 'object' ? l.target.id : l.target;
    if (highlightedNodes.size > 0 && highlightedNodes.has(src) && highlightedNodes.has(tgt)) {
        return isDarkMode ? '#fbbf24' : '#d97706';
    }
    if (l.critical) return isDarkMode ? '#ec4899' : '#db2777';
    return isDarkMode ? '#44475a' : '#475569';
}

const Graph = ForceGraph()(container)
    .onDagError(() => handleDagCycleError())
    .graphData(JSON.parse(JSON.stringify(DATA)))
    .backgroundColor('transparent')
    .nodeId('id')
    .nodeLabel(null)
    .nodeColor(computeNodeColor)
    .nodeVal(n => getNodeSize(n))
    .linkColor(computeLinkColor)
    .linkWidth(l => {
        const src = typeof l.source === 'object' ? l.source.id : l.source;
        const tgt = typeof l.target === 'object' ? l.target.id : l.target;
        if (highlightedNodes.size > 0 && highlightedNodes.has(src) && highlightedNodes.has(tgt)) return 3;
        return l.critical ? 2 : 1;
    })
    .linkDirectionalArrowLength(5)
    .linkDirectionalArrowColor(computeLinkArrowColor)
    .linkDirectionalArrowRelPos(1)
    .linkCurvature(0.1)
    .linkDirectionalParticles(l => l.critical ? 2 : 0)
    .linkDirectionalParticleSpeed(0.003)
    .linkDirectionalParticleWidth(2)
    .linkDirectionalParticleColor(() => '#ec4899')
    .d3AlphaDecay(0.02)
    .d3VelocityDecay(0.25)
    .nodeCanvasObject((node, ctx, globalScale) => {
        const x = node.x, y = node.y;
        if (x === undefined || y === undefined || !isFinite(x) || !isFinite(y)) return;
        const size = getNodeSize(node);
        const baseColor = computeNodeBaseColor(node);
        const isHighlighted = highlightedNodes.size === 0 || highlightedNodes.has(node.id);
        const isHovered = hoveredNode && hoveredNode.id === node.id;
        const alpha = isHighlighted ? 1 : 0.15;

        // Golden glow for hovered node's connected subgraph
        if (isHovered || (highlightedNodes.has(node.id) && highlightedNodes.size > 0)) {
            ctx.beginPath(); ctx.arc(x, y, size + 8, 0, 2 * Math.PI);
            const g = ctx.createRadialGradient(x, y, size, x, y, size + 12);
            g.addColorStop(0, 'rgba(251, 191, 36, 0.6)'); g.addColorStop(1, 'transparent');
            ctx.fillStyle = g; ctx.fill();
        }

        // Historical diff halo (--diff-since overlay, W32-3)
        if (node.diff_status) {
            ctx.save();
            ctx.globalAlpha = alpha;
            ctx.beginPath();
            ctx.arc(x, y, size + 5, 0, 2 * Math.PI);
            if (node.diff_status === 'added') {
                ctx.strokeStyle = '#22c55e';
                ctx.lineWidth = 2.5 / Math.max(0.5, globalScale);
            } else if (node.diff_status === 'reopened' || node.diff_status === 'modified') {
                ctx.strokeStyle = '#f59e0b';
                ctx.lineWidth = 2.2 / Math.max(0.5, globalScale);
            } else if (node.diff_status === 'closed') {
                ctx.strokeStyle = '#94a3b8';
                ctx.setLineDash([3 / Math.max(0.5, globalScale), 2 / Math.max(0.5, globalScale)]);
                ctx.lineWidth = 2 / Math.max(0.5, globalScale);
            }
            ctx.stroke();
            ctx.restore();
        }

        // Articulation point glow (active issues only)
        if (isActiveNodeStatus(node.status) && node.is_articulation && isHighlighted) {
            ctx.beginPath(); ctx.arc(x, y, size + 6, 0, 2 * Math.PI);
            const g = ctx.createRadialGradient(x, y, size, x, y, size + 8);
            g.addColorStop(0, '#ec489960'); g.addColorStop(1, 'transparent');
            ctx.fillStyle = g; ctx.fill();
        }

        // Critical path indicator (active issues only)
        if (node.slack_available !== false && isActiveNodeStatus(node.status) && node.slack === 0 && isHighlighted) {
            ctx.save();
            ctx.globalAlpha = 0.2;
            ctx.beginPath(); ctx.arc(x, y, size + 3, 0, 2 * Math.PI);
            ctx.fillStyle = baseColor; ctx.fill();
            ctx.restore();
        }

        // Priority ring
        const pColor = PRIORITY_COLORS[node.priority] || PRIORITY_COLORS[2];
        ctx.globalAlpha = alpha;
        ctx.beginPath(); ctx.arc(x, y, size + 1.5, 0, 2 * Math.PI);
        ctx.strokeStyle = pColor; ctx.lineWidth = 2; ctx.stroke();

        // Node shape based on type
        ctx.fillStyle = baseColor;
        ctx.beginPath();
        switch(node.type) {
            case 'bug': // Triangle
                ctx.moveTo(x, y - size);
                ctx.lineTo(x + size * 0.866, y + size * 0.5);
                ctx.lineTo(x - size * 0.866, y + size * 0.5);
                ctx.closePath();
                break;
            case 'task': // Square
                ctx.rect(x - size * 0.7, y - size * 0.7, size * 1.4, size * 1.4);
                break;
            case 'epic': // Diamond
                ctx.moveTo(x, y - size);
                ctx.lineTo(x + size, y);
                ctx.lineTo(x, y + size);
                ctx.lineTo(x - size, y);
                ctx.closePath();
                break;
            default: // Circle (feature)
                ctx.arc(x, y, size, 0, 2 * Math.PI);
        }
        ctx.fill();

        // Highlight effect
        const hl = ctx.createRadialGradient(x - size/3, y - size/3, 0, x, y, size);
        hl.addColorStop(0, 'rgba(255,255,255,0.2)'); hl.addColorStop(1, 'transparent');
        ctx.fillStyle = hl; ctx.fill();
        ctx.globalAlpha = 1;

        // Labels at zoom
        if (globalScale > 1.2 && isHighlighted) {
            const fontSize = Math.max(10 / globalScale, 3);
            ctx.font = fontSize + 'px Inter, sans-serif';
            ctx.textAlign = 'center'; ctx.textBaseline = 'middle';
            ctx.fillStyle = document.body.classList.contains('light-mode') ? '#1a1a2e' : '#e8e8f0';
            ctx.fillText(node.id, x, y + size + fontSize + 2);
            if (globalScale > 2) {
                ctx.fillStyle = pColor;
                ctx.font = (fontSize * 0.8) + 'px JetBrains Mono, monospace';
                ctx.fillText('P' + node.priority, x, y);
            }
        }
    })
    .nodePointerAreaPaint((n, c, ctx) => {
        if (n.x === undefined || n.y === undefined || !isFinite(n.x) || !isFinite(n.y)) return;
        const size = getNodeSize(n) + 4;
        ctx.fillStyle = c; ctx.beginPath(); ctx.arc(n.x, n.y, size, 0, 2 * Math.PI); ctx.fill();
    })
    .onNodeClick(handleNodeClick)
    .onNodeRightClick((node, event) => { event.preventDefault(); showContextMenu(node, event); })
    .onNodeHover(handleNodeHover)
    .onBackgroundClick(() => { clearSelection(); hideContextMenu(); hideHoverPanel(); })
    .onBackgroundRightClick(() => hideContextMenu());

// Hover handling with golden glow and detail panel
function handleNodeHover(node) {
    hoveredNode = node;
    container.style.cursor = node ? 'pointer' : 'grab';
    if (node) {
        highlightedNodes = activePathNodes ? new Set(activePathNodes) : getConnectedNodes(node.id, 2);
        showHoverPanel(node);
    } else {
        highlightedNodes = activePathNodes ? new Set(activePathNodes) : new Set();
        hideHoverPanel();
    }
    Graph.nodeColor(Graph.nodeColor()); // Trigger re-render
    Graph.linkColor(Graph.linkColor());
    Graph.linkWidth(Graph.linkWidth());
}

// Panel mode: 'docked' (left sidebar) or 'floating' (center overlay)
let panelMode = 'docked';

// Populate panel content for a given prefix (hover- or docked-)
function populatePanelContent(prefix, node) {
    document.getElementById(prefix + 'id').textContent = node.id;
    document.getElementById(prefix + 'title').textContent = node.title;

    // Type badge
    const typeBadge = document.getElementById(prefix + 'type-badge');
    typeBadge.textContent = node.type || 'task';
    typeBadge.className = 'hover-type-badge badge-' + safeClassToken(node.type || 'task');

    // Badges
    const badgesEl = document.getElementById(prefix + 'badges');
    badgesEl.innerHTML = '';
    const addBadge = (cls, text) => { const b = document.createElement('span'); b.className = 'badge ' + cls; b.textContent = text; badgesEl.appendChild(b); };
    addBadge('badge-' + safeClassToken(node.status), node.status.replace('_', ' '));
    addBadge('', 'P' + node.priority);
    if (isActiveNodeStatus(node.status) && node.is_articulation) addBadge('badge-articulation', 'Cut Vertex');
    if (node.slack_available !== false && isActiveNodeStatus(node.status) && node.slack === 0) addBadge('badge-critical', 'Critical Path');
    if (node.diff_status) {
        const diffLabels = { added: '+ Added', closed: '✓ Closed', reopened: '↺ Reopened', modified: '~ Modified' };
        let diffText = diffLabels[node.diff_status] || ('Δ ' + node.diff_status);
        if (node.diff_status === 'modified' && Array.isArray(node.diff_fields) && node.diff_fields.length > 0) {
            diffText += ' (' + node.diff_fields.slice(0, 3).join(', ') + ')';
        }
        addBadge('badge-type', diffText);
    }
    (node.labels || []).forEach(l => addBadge('badge-type', l));

    // Description
    const descSection = document.getElementById(prefix + 'description');
    if (node.description) {
        descSection.style.display = 'block';
        document.getElementById(prefix + 'description-content').replaceChildren(renderSafeMarkdownFragment(node.description));
    } else { descSection.style.display = 'none'; }

    // Design
    const designSection = document.getElementById(prefix + 'design');
    if (node.design) {
        designSection.style.display = 'block';
        document.getElementById(prefix + 'design-content').replaceChildren(renderSafeMarkdownFragment(node.design));
    } else { designSection.style.display = 'none'; }

    // Acceptance Criteria
    const acSection = document.getElementById(prefix + 'acceptance');
    if (node.acceptance_criteria) {
        acSection.style.display = 'block';
        document.getElementById(prefix + 'acceptance-content').replaceChildren(renderSafeMarkdownFragment(node.acceptance_criteria));
    } else { acSection.style.display = 'none'; }

    // Notes
    const notesSection = document.getElementById(prefix + 'notes');
    if (node.notes) {
        notesSection.style.display = 'block';
        document.getElementById(prefix + 'notes-content').replaceChildren(renderSafeMarkdownFragment(node.notes));
    } else { notesSection.style.display = 'none'; }

    // Metadata
    const metaEl = document.getElementById(prefix + 'meta');
    metaEl.innerHTML = '';
    const addMeta = (label, value) => {
        if (!value) return;
        metaEl.innerHTML += '<div class="hover-meta-item"><span class="hover-meta-label">' + escapeHTML(label) + '</span><span class="hover-meta-value">' + escapeHTML(value) + '</span></div>';
    };
    const formatEstimate = (mins) => {
        const m = Number(mins);
        if (!isFinite(m) || m <= 0) return '';
        if (m < 60) return m + 'm';
        const h = Math.floor(m / 60), rm = m %% 60;
        if (h < 24) return rm ? (h + 'h ' + rm + 'm') : (h + 'h');
        const d = Math.floor(h / 24), rh = h %% 24;
        return rh ? (d + 'd ' + rh + 'h') : (d + 'd');
    };
    addMeta('Assignee', node.assignee);
    addMeta('Repo', node.source_repo);
    addMeta('Estimate', formatEstimate(node.estimated_minutes));
    if (isActiveNodeStatus(node.status) && node.eta_days != null && node.eta_date) {
        const etaDaysNum = Number(node.eta_days);
        const etaConfPct = Math.round(Number(node.eta_confidence ?? 0) * 100);
        if (isFinite(etaDaysNum)) {
            addMeta('ETA Forecast', '~' + etaDaysNum.toFixed(1) + 'd (' + node.eta_date + ' · ' + etaConfPct + '%% conf)');
        }
    }
    if (isActiveNodeStatus(node.status) && node.suggested_priority != null && node.suggested_priority !== node.priority) {
        const arrow = node.priority_direction === 'increase' ? '↑ ' : '↓ ';
        const reasonPart = node.priority_reason ? (' · ' + node.priority_reason) : '';
        addMeta('Priority Hint', arrow + 'P' + node.priority + ' → P' + node.suggested_priority + reasonPart);
    }
    addMeta('Created By', node.created_by);
    addMeta('Created', node.created_at);
    addMeta('Updated', node.updated_at);
    addMeta('Due Date', node.due_date);
    addMeta('Deferred Until', node.defer_until);
    addMeta('External Ref', node.external_ref);
    if (node.closed_at) addMeta('Closed', node.closed_at);
    if (node.close_reason) addMeta('Close Reason', node.close_reason);
    if (node.last_author) addMeta('Last Author', node.last_author);
    if (node.commit_count > 0) addMeta('Commits', String(node.commit_count));
    if (DATA.git_range) addMeta('Git Range', DATA.git_range);

    // Root Blockers ("Start Here") for blocked active issues
    const rootBlkSection = document.getElementById(prefix + 'root-blockers');
    const rootBlkTitle = document.getElementById(prefix + 'root-blockers-title');
    const rootBlkList = document.getElementById(prefix + 'root-blockers-list');
    if (rootBlkSection && rootBlkList) {
        const rbInfo = computeStandaloneRootBlockers(node.id);
        if (rbInfo && (rbInfo.roots.length > 0 || rbInfo.hasCycle)) {
            rootBlkSection.style.display = 'block';
            if (rootBlkTitle) {
                rootBlkTitle.textContent = '🎯 Start Here: Root Blockers' + (rbInfo.chainLength > 0 ? (' (' + rbInfo.chainLength + ' upstream)') : '');
            }
            const chips = rbInfo.roots.slice(0, 8).map(r => {
                const depthSuffix = r.depth > 1 ? (' · d' + r.depth) : '';
                const actPrefix = r.actionable ? '▶ ' : '';
                const titleAttr = escapeHTML((r.title || r.id) + ' (P' + (r.priority ?? 2) + ', ' + (r.actionable ? 'actionable' : r.status) + ')');
                return '<span class="hover-dep-chip" data-id="' + escapeHTML(r.id) + '" title="' + titleAttr + '">' + actPrefix + escapeHTML(r.id) + escapeHTML(depthSuffix) + '</span>';
            });
            if (rbInfo.hasCycle) {
                chips.push('<span class="badge badge-blocked" title="Dependency cycle prevents full unblocking">⚠ cycle</span>');
            }
            rootBlkList.innerHTML = chips.join('');
        } else {
            rootBlkSection.style.display = 'none';
            rootBlkList.innerHTML = '';
        }
    }

    // Blocked By
    const blockedBySection = document.getElementById(prefix + 'blocked-by');
    const blockedByList = document.getElementById(prefix + 'blocked-by-list');
    if (node.blocked_by && node.blocked_by.length > 0) {
        blockedBySection.style.display = 'block';
        blockedByList.innerHTML = node.blocked_by.map(id => '<span class="hover-dep-chip" data-id="' + escapeHTML(id) + '">' + escapeHTML(id) + '</span>').join('');
    } else { blockedBySection.style.display = 'none'; }

    // Blocks
    const blocksSection = document.getElementById(prefix + 'blocks');
    const blocksList = document.getElementById(prefix + 'blocks-list');
    if (node.blocks && node.blocks.length > 0) {
        blocksSection.style.display = 'block';
        blocksList.innerHTML = node.blocks.map(id => '<span class="hover-dep-chip" data-id="' + escapeHTML(id) + '">' + escapeHTML(id) + '</span>').join('');
    } else { blocksSection.style.display = 'none'; }

    // Commits
    const commitsSection = document.getElementById(prefix + 'commits');
    const commitsList = document.getElementById(prefix + 'commits-list');
    if ((node.commits && node.commits.length > 0) || node.commit_count > 0) {
        commitsSection.style.display = 'block';
        const headerParts = [];
        if (node.commit_count > 0) headerParts.push(escapeHTML(node.commit_count) + (node.commit_count === 1 ? ' commit' : ' commits'));
        if (node.last_author) headerParts.push('last by ' + escapeHTML(node.last_author));
        if (DATA.git_range) headerParts.push('range ' + escapeHTML(DATA.git_range));
        const summaryHtml = headerParts.length > 0 ? '<div class="hover-commit"><span class="hover-commit-msg">' + headerParts.join(' · ') + '</span></div>' : '';
        const rowsHtml = (node.commits || []).slice(0, 5).map(c => '<div class="hover-commit"><span class="hover-commit-sha">' + escapeHTML(c.short_sha) + '</span> <span class="hover-commit-msg">' + escapeHTML((c.message || '').split(/\r?\n/)[0].substring(0, 60)) + '</span></div>').join('');
        commitsList.innerHTML = summaryHtml + rowsHtml;
    } else { commitsSection.style.display = 'none'; }

    // Metrics
    const metricsEl = document.getElementById(prefix + 'metrics');
    metricsEl.innerHTML = '';
    const addMetric = (label, value) => {
        metricsEl.innerHTML += '<div class="hover-meta-item"><span class="hover-meta-label">' + escapeHTML(label) + '</span><span class="hover-meta-value">' + escapeHTML(value) + '</span></div>';
    };
    const fmt = (v, d) => (v != null && isFinite(v)) ? v.toFixed(d) : '-';
    addMetric('PageRank', fmt(node.pagerank * 100, 3) + '%%');
    addMetric('PR Rank', '#' + (node.pagerank_rank || '-'));
    addMetric('Betweenness', fmt(node.betweenness, 4));
    addMetric('BW Rank', '#' + (node.betweenness_rank || '-'));
    addMetric('Eigenvector', fmt(node.eigenvector, 4));
    addMetric('HITS Hub', fmt(node.hub, 4));
    addMetric('HITS Authority', fmt(node.authority, 4));
    addMetric('K-Core', node.core_number ?? '-');
    addMetric('Critical Path', fmt(node.critical_path, 1));
    addMetric('Slack', node.slack_available === false ? (DATA.has_cycles ? 'N/A (cycle)' : 'N/A') : fmt(node.slack, 1));
    addMetric('In-Degree', node.in_degree ?? '-');
    addMetric('Out-Degree', node.out_degree ?? '-');
    if (isActiveNodeStatus(node.status) && node.risk_score > 0) {
        const riskPct = fmt(node.risk_score * 100, 0) + '%%';
        addMetric('Risk', node.risk_explanation ? riskPct + ' (' + node.risk_explanation + ')' : riskPct);
    }
}

// Focus a visible node or inform the user when hidden by active filters
function focusVisibleGraphNode(targetId) {
    if (!targetId) return;
    const graphNodes = Graph.graphData().nodes || [];
    const target = graphNodes.find(n => n.id === targetId);
    if (!target) return;
    if (!currentVisibilityFilter(target)) {
        showToast(target.id + ' is hidden by active filters');
        return;
    }
    selectNode(target);
    if (Number.isFinite(target.x) && Number.isFinite(target.y)) {
        Graph.centerAt(target.x, target.y, 500);
        Graph.zoom(2.5, 500);
    }
}

// Wire up dep chip clicks for a container
function wireDepChips(container) {
    container.querySelectorAll('.hover-dep-chip').forEach(chip => {
        chip.onclick = () => focusVisibleGraphNode(chip.dataset.id);
    });
}

// Show full details panel (docked or floating based on mode)
let activePanelNode = null;
function showHoverPanel(node) {
    activePanelNode = node || null;
    if (panelMode === 'docked') {
        // Update docked panel
        populatePanelContent('docked-', node);
        document.getElementById('docked-panel').classList.add('visible');
        document.getElementById('docked-no-selection').style.display = 'none';
        wireDepChips(document.getElementById('docked-panel'));
    } else {
        // Update floating panel
        populatePanelContent('hover-', node);
        document.getElementById('hover-panel').classList.add('visible');
        wireDepChips(document.getElementById('hover-panel'));
    }
}

function hideHoverPanel() {
    activePanelNode = null;
    if (panelMode === 'docked') {
        document.getElementById('docked-panel').classList.remove('visible');
        document.getElementById('docked-no-selection').style.display = 'flex';
    } else {
        document.getElementById('hover-panel').classList.remove('visible');
    }
}

// Toggle between docked and floating modes
function togglePanelMode() {
    const detachBtn = document.getElementById('btn-detach');
    const sidebar = document.getElementById('detail-sidebar');

    if (panelMode === 'docked') {
        panelMode = 'floating';
        sidebar.classList.add('collapsed');
        detachBtn.innerHTML = '<span>⇲</span> Dock';
        detachBtn.title = 'Dock panel to left side (D)';
        // Show floating panel if we have a hovered node
        if (hoveredNode) {
            populatePanelContent('hover-', hoveredNode);
            document.getElementById('hover-panel').classList.add('visible');
            wireDepChips(document.getElementById('hover-panel'));
        }
    } else {
        panelMode = 'docked';
        sidebar.classList.remove('collapsed');
        detachBtn.innerHTML = '<span>⇱</span> Detach';
        detachBtn.title = 'Detach panel to float (D)';
        document.getElementById('hover-panel').classList.remove('visible');
        // Show docked panel if we have a hovered node
        if (hoveredNode) {
            populatePanelContent('docked-', hoveredNode);
            document.getElementById('docked-panel').classList.add('visible');
            document.getElementById('docked-no-selection').style.display = 'none';
            wireDepChips(document.getElementById('docked-panel'));
        }
    }
}

document.getElementById('btn-detach').onclick = togglePanelMode;
document.getElementById('hover-close').onclick = () => document.getElementById('hover-panel').classList.remove('visible');

let selectedNode = null;
function selectNode(node) {
    if (!node) return;
    // P20B-3: Clear any active What-If, Check Add Dep, Cycle, or Path overlay on normal selection
    // so stale banners and dimmed 0.15-opacity states do not remain stuck over the newly selected node.
    if (activeWhatIfIds.length > 0 || activeWhatIfId) {
        clearWhatIfSimulation();
    }
    if (checkDepMode || checkDepFromId) {
        cancelCheckDep();
    }
    if (typeof closeCycleInspector === 'function') {
        closeCycleInspector();
    }
    if (activePathNodes || highlightedNodes.size > 0) {
        activePathNodes = null;
        highlightedNodes = new Set();
        Graph.nodeColor(Graph.nodeColor());
        Graph.linkColor(Graph.linkColor());
        Graph.linkWidth(Graph.linkWidth());
    }
    selectedNode = node;
    addToRecent(node);
    showHoverPanel(node);
    document.getElementById('detail-id').textContent = node.id;
    document.getElementById('detail-name').textContent = node.title;
    const prioEl = document.getElementById('detail-priority');
    prioEl.textContent = 'P' + node.priority;
    prioEl.style.background = PRIORITY_COLORS[node.priority];
    prioEl.style.color = node.priority <= 1 ? 'white' : '#0f0f1a';
    const badgesEl = document.getElementById('detail-badges');
    badgesEl.innerHTML = '';
    const sb = document.createElement('span'); sb.className = 'badge badge-' + safeClassToken(node.status); sb.textContent = node.status.replace('_', ' '); badgesEl.appendChild(sb);
    const tb = document.createElement('span'); tb.className = 'badge badge-' + safeClassToken(node.type || 'task'); tb.textContent = node.type || 'task'; badgesEl.appendChild(tb);
    const fmtSide = (v, d) => (v != null && isFinite(v)) ? v.toFixed(d) : '-';
    document.getElementById('m-pagerank').textContent = fmtSide(node.pagerank * 100, 2) + '%%';
    document.getElementById('m-prrank').textContent = '#' + (node.pagerank_rank || '-');
    document.getElementById('m-between').textContent = fmtSide(node.betweenness, 4);
    document.getElementById('m-bwrank').textContent = '#' + (node.betweenness_rank || '-');
    document.getElementById('m-critical').textContent = fmtSide(node.critical_path, 1);
    const slackEl = document.getElementById('m-slack');
    slackEl.textContent = node.slack_available === false ? (DATA.has_cycles ? 'N/A (cycle)' : 'N/A') : fmtSide(node.slack, 1);
    slackEl.className = 'metric-value' + (node.slack_available !== false && isActiveNodeStatus(node.status) && node.slack === 0 ? ' highlight' : '');
    document.getElementById('m-indeg').textContent = node.in_degree ?? '-';
    document.getElementById('m-outdeg').textContent = node.out_degree ?? '-';
    const whatIfBtn = document.getElementById('btn-detail-whatif');
    if (whatIfBtn) {
        whatIfBtn.style.display = isActiveNodeStatus(node.status) ? '' : 'none';
    }
    const blockersWrap = document.getElementById('detail-blockers-wrap');
    const blockersList = document.getElementById('detail-blockers-list');
    if (blockersWrap && blockersList) {
        blockersList.textContent = '';
        const model = buildStandaloneWhatIfModel();
        const directSet = new Set();
        DATA.links.forEach(l => {
            const src = typeof l.source === 'object' ? l.source.id : l.source;
            const tgt = typeof l.target === 'object' ? l.target.id : l.target;
            if (src === node.id && tgt && isBlockingLinkType(l.type || 'blocks')) {
                directSet.add(tgt);
            }
        });
        if (Array.isArray(node.blocked_by)) {
            const openParents = model.openParents.get(node.id);
            node.blocked_by.forEach(bId => {
                if (!bId || !model.allNodes.has(bId)) return;
                const isParent = model.parentChildEdges && model.parentChildEdges.has(node.id + '\0' + bId);
                if (!isParent && (!openParents || !openParents.has(bId))) {
                    directSet.add(bId);
                }
            });
        }
        const blockers = [...directSet].filter(bId => model.allNodes.has(bId)).sort();
        if (blockers.length > 0) {
            blockers.forEach(bId => {
                const blkNode = model.allNodes.get(bId);
                const chip = document.createElement('span');
                chip.className = 'detail-blocker-chip';
                chip.style.display = 'inline-flex';
                chip.style.alignItems = 'center';
                chip.style.gap = '0.2rem';
                chip.style.padding = '0.1rem 0.3rem';
                chip.style.borderRadius = '4px';
                chip.style.background = 'rgba(245, 158, 11, 0.14)';
                chip.style.border = '1px solid rgba(245, 158, 11, 0.35)';
                chip.style.fontSize = '0.7rem';

                const navBtn = document.createElement('button');
                navBtn.type = 'button';
                navBtn.className = 'btn-detail-blocker-nav';
                navBtn.textContent = bId;
                navBtn.style.background = 'transparent';
                navBtn.style.border = 'none';
                navBtn.style.color = 'inherit';
                navBtn.style.cursor = 'pointer';
                navBtn.style.padding = '0';
                navBtn.style.fontFamily = 'monospace';
                if (blkNode && !isActiveNodeStatus(blkNode.status)) {
                    navBtn.style.opacity = '0.6';
                    navBtn.style.textDecoration = 'line-through';
                }
                navBtn.title = 'Focus blocker ' + bId;
                navBtn.onclick = (e) => {
                    e.stopPropagation();
                    focusVisibleGraphNode(bId);
                };

                const cutBtn = document.createElement('button');
                cutBtn.type = 'button';
                cutBtn.className = 'btn-detail-remove-blocker';
                cutBtn.dataset.removeFrom = node.id;
                cutBtn.dataset.removeTo = bId;
                cutBtn.textContent = '✂';
                cutBtn.style.background = 'transparent';
                cutBtn.style.border = 'none';
                cutBtn.style.borderLeft = '1px solid rgba(245, 158, 11, 0.35)';
                cutBtn.style.padding = '0 0 0 0.25rem';
                cutBtn.style.color = '#f59e0b';
                cutBtn.style.cursor = 'pointer';
                cutBtn.title = 'Preflight removing blocking dependency ' + node.id + ' → ' + bId;
                cutBtn.setAttribute('aria-label', 'Preflight remove blocker ' + bId);
                cutBtn.onclick = (e) => {
                    e.stopPropagation();
                    runStandaloneCheckDepRemoval(node.id, bId);
                };

                chip.appendChild(navBtn);
                chip.appendChild(cutBtn);
                blockersList.appendChild(chip);
            });
            blockersWrap.style.display = 'block';
        } else {
            blockersWrap.style.display = 'none';
        }
    }
    document.getElementById('node-detail').classList.add('visible');
    document.getElementById('no-selection').style.display = 'none';
}

function clearSelection() {
    selectedNode = null;
    activePathNodes = null;
    highlightedNodes = new Set();
    closeCycleInspector();
    cancelCheckDep();
    clearWhatIfSimulation();
    const blockersWrap = document.getElementById('detail-blockers-wrap');
    const blockersList = document.getElementById('detail-blockers-list');
    if (blockersList) blockersList.textContent = '';
    if (blockersWrap) blockersWrap.style.display = 'none';
    document.getElementById('node-detail').classList.remove('visible');
    document.getElementById('no-selection').style.display = 'block';
    Graph.nodeColor(Graph.nodeColor());
    Graph.linkColor(Graph.linkColor());
    Graph.linkWidth(Graph.linkWidth());
}

// Full-text search
let searchQuery = '';
let searchDebounce = null;
document.getElementById('search-input').oninput = e => {
    searchQuery = e.target.value || '';
    clearTimeout(searchDebounce);
    searchDebounce = setTimeout(() => performSearch(e.target.value), 150);
};

function performSearch(query) {
    searchQuery = query || '';
    const resultsEl = document.getElementById('search-results');
    if (!query || query.length < 2) {
        resultsEl.classList.remove('visible');
        applyFilters();
        return;
    }
    applyFilters();
    const q = query.toLowerCase();
    const matches = DATA.nodes.filter(n => {
        return n.id.toLowerCase().includes(q) ||
               n.title.toLowerCase().includes(q) ||
               (n.description || '').toLowerCase().includes(q) ||
               (n.design || '').toLowerCase().includes(q) ||
               (n.notes || '').toLowerCase().includes(q) ||
               (n.acceptance_criteria || '').toLowerCase().includes(q) ||
               (n.labels || []).some(l => l.toLowerCase().includes(q)) ||
               (n.assignee || '').toLowerCase().includes(q) ||
               (n.source_repo || '').toLowerCase().includes(q) ||
               (n.created_by || '').toLowerCase().includes(q) ||
               (n.close_reason || '').toLowerCase().includes(q);
    }).slice(0, 8);

    if (matches.length === 0) {
        resultsEl.innerHTML = '<div class="search-result-item">No results found</div>';
    } else {
        resultsEl.innerHTML = matches.map(n => {
            let preview = '';
            const fields = [n.description, n.design, n.notes, n.acceptance_criteria];
            for (const f of fields) {
                if (f && f.toLowerCase().includes(q)) {
                    const idx = f.toLowerCase().indexOf(q);
                    const start = Math.max(0, idx - 30);
                    const end = Math.min(f.length, idx + q.length + 50);
                    preview = '...' + highlightMatches(f.substring(start, end), query) + '...';
                    break;
                }
            }
            return '<div class="search-result-item" data-id="' + escapeHTML(n.id) + '">' +
                   '<div class="search-result-id">' + escapeHTML(n.id) + ' <span class="badge badge-' + safeClassToken(n.status) + '">' + escapeHTML(n.status) + '</span></div>' +
                   '<div class="search-result-title">' + escapeHTML(n.title) + '</div>' +
                   (preview ? '<div class="search-result-preview">' + preview + '</div>' : '') +
                   '</div>';
        }).join('');
    }
    resultsEl.classList.add('visible');

    resultsEl.querySelectorAll('.search-result-item[data-id]').forEach(item => {
        item.onclick = () => {
            const id = item.dataset.id;
            resultsEl.classList.remove('visible');
            searchQuery = '';
            document.getElementById('search-input').value = '';
            applyFilters();
            focusVisibleGraphNode(id);
        };
    });
}

// Close search on click outside
document.addEventListener('click', e => {
    if (!e.target.closest('.search-container')) {
        document.getElementById('search-results').classList.remove('visible');
    }
});

// Context menu
let contextNode = null;
function showContextMenu(node, event) {
    contextNode = node;
    const whatIfItem = document.getElementById('ctx-whatif');
    if (whatIfItem) {
        whatIfItem.style.display = (node && isActiveNodeStatus(node.status)) ? '' : 'none';
    }
    const whatIfBatchItem = document.getElementById('ctx-whatif-batch');
    if (whatIfBatchItem) {
        whatIfBatchItem.style.display = (node && isActiveNodeStatus(node.status)) ? '' : 'none';
    }
    const menu = document.getElementById('context-menu');
    menu.style.left = '0px';
    menu.style.top = '0px';
    menu.classList.add('visible');
    const menuW = menu.offsetWidth || 220;
    const menuH = menu.offsetHeight || 280;
    const pad = 8;
    const maxLeft = Math.max(pad, window.innerWidth - menuW - pad);
    const maxTop = Math.max(pad, window.innerHeight - menuH - pad);
    menu.style.left = Math.max(pad, Math.min(event.clientX, maxLeft)) + 'px';
    menu.style.top = Math.max(pad, Math.min(event.clientY, maxTop)) + 'px';
}
function hideContextMenu() { document.getElementById('context-menu').classList.remove('visible'); contextNode = null; }
async function writeClipboardText(value) {
    if (navigator.clipboard && window.isSecureContext) {
        try {
            await navigator.clipboard.writeText(value);
            return;
        } catch (_) {
            // Permission policies can reject the modern API even in a secure
            // context. Fall through to the synchronous compatibility path.
        }
    }

    const textarea = document.createElement('textarea');
    textarea.value = value;
    textarea.setAttribute('readonly', '');
    textarea.style.position = 'fixed';
    textarea.style.opacity = '0';
    document.body.appendChild(textarea);
    textarea.select();
    try {
        if (!document.execCommand('copy')) throw new Error('copy command rejected');
    } finally {
        textarea.remove();
    }
}
async function copyContextField(value, fieldName) {
    const text = String(value ?? '');
    if (!text.trim()) {
        showToast('No ' + fieldName.toLowerCase() + ' to copy');
        return;
    }

    try {
        await writeClipboardText(text);
        showToast(fieldName + ' copied');
    } catch (_) {
        showToast('Copy failed');
    }
}
document.getElementById('ctx-focus').onclick = () => { if (contextNode) { Graph.centerAt(contextNode.x, contextNode.y, 500); Graph.zoom(3, 500); } hideContextMenu(); };
document.getElementById('ctx-details').onclick = () => { if (contextNode) showHoverPanel(contextNode); hideContextMenu(); };
document.getElementById('ctx-deps').onclick = () => { if (contextNode) { cancelPathFinder(); cancelCheckDep(); closeCycleInspector(); clearWhatIfSimulation(); highlightDependencies(contextNode, 'deps'); } hideContextMenu(); };
document.getElementById('ctx-dependents').onclick = () => { if (contextNode) { cancelPathFinder(); cancelCheckDep(); closeCycleInspector(); clearWhatIfSimulation(); highlightDependencies(contextNode, 'dependents'); } hideContextMenu(); };
document.getElementById('ctx-connected').onclick = () => {
    if (contextNode) {
        cancelPathFinder();
        cancelCheckDep();
        closeCycleInspector();
        clearWhatIfSimulation();
        const connected = getConnectedNodes(contextNode.id, 3);
        activePathNodes = connected;
        highlightedNodes = connected;
        Graph.nodeColor(Graph.nodeColor());
        Graph.linkColor(Graph.linkColor());
        Graph.linkWidth(Graph.linkWidth());
        showToast(highlightedNodes.size + ' connected nodes highlighted');
    }
    hideContextMenu();
};
document.getElementById('ctx-whatif').onclick = () => {
    if (contextNode && isActiveNodeStatus(contextNode.status)) runWhatIfSimulation(contextNode.id);
    hideContextMenu();
};
document.getElementById('ctx-whatif-batch').onclick = () => {
    if (contextNode) {
        if (!isActiveNodeStatus(contextNode.status)) {
            showToast('What-If only simulates closing active issues');
        } else {
            const idx = activeWhatIfIds.indexOf(contextNode.id);
            const nextIds = idx >= 0
                ? activeWhatIfIds.filter(id => id !== contextNode.id)
                : [...activeWhatIfIds, contextNode.id];
            runWhatIfBatchSimulation(nextIds);
        }
    }
    hideContextMenu();
};
document.getElementById('ctx-checkdep').onclick = () => {
    if (contextNode) startStandaloneCheckDep(contextNode.id);
    hideContextMenu();
};

function cancelPathFinder() {
    if (!pathFinderMode) return;
    pathFinderMode = false;
    pathStart = null;
    const banner = document.getElementById('pathfinder-banner');
    if (banner) banner.classList.remove('visible');
    const btn = document.getElementById('btn-path');
    if (btn) btn.classList.remove('active');
}

// Pure-JS active-blocker What-If close cascade simulation (matching readiness.go / viewer.js)
let activeWhatIfId = null;
function isWhatIfUnlockableNode(n) {
    if (!n) return false;
    const st = String(n.status || '').trim().toLowerCase();
    if (st !== 'open' && st !== 'in_progress') return false;
    return !isNodeWithheldFromReady(n);
}
let _standaloneWhatIfCache = null;
function buildStandaloneWhatIfModel() {
    if (_standaloneWhatIfCache &&
        _standaloneWhatIfCache.nodesRef === DATA.nodes &&
        _standaloneWhatIfCache.linksRef === DATA.links &&
        _standaloneWhatIfCache.nodesLen === DATA.nodes.length &&
        _standaloneWhatIfCache.linksLen === DATA.links.length) {
        return _standaloneWhatIfCache.model;
    }
    const allNodes = new Map();
    const activeNodes = new Map();
    DATA.nodes.forEach(n => {
        if (!n || !n.id) return;
        allNodes.set(n.id, n);
        if (isActiveNodeStatus(n.status)) activeNodes.set(n.id, n);
    });
    const directBlockers = new Map();
    const directDependents = new Map();
    const openParents = new Map();
    const openChildren = new Map();
    const unresolvableBlockers = new Set();
    const selfLoops = new Set();
    const blockingSelfLoops = new Set();
    const parentChildSelfLoops = new Set();
    const otherUnresolvableBlockers = new Set();
    const parentChildEdges = new Set();
    activeNodes.forEach((_, id) => {
        directBlockers.set(id, new Set());
        directDependents.set(id, new Set());
        openParents.set(id, new Set());
        openChildren.set(id, new Set());
    });
    DATA.links.forEach(l => {
        const src = typeof l.source === 'object' ? l.source.id : l.source;
        const tgt = typeof l.target === 'object' ? l.target.id : l.target;
        if (!activeNodes.has(src)) return;
        const ltype = l.type || 'blocks';
        if (ltype === 'parent-child') {
            parentChildEdges.add(src + '\0' + tgt);
            if (src === tgt) {
                unresolvableBlockers.add(src);
                parentChildSelfLoops.add(src);
                selfLoops.add(src);
            } else if (activeNodes.has(tgt)) {
                openParents.get(src).add(tgt);
                openChildren.get(tgt).add(src);
            } else if (!allNodes.has(tgt)) {
                unresolvableBlockers.add(src);
                otherUnresolvableBlockers.add(src);
            }
            return;
        }
        if (ltype !== 'blocks' && ltype !== 'conditional-blocks' && ltype !== 'waits-for') return;
        if (src === tgt) {
            unresolvableBlockers.add(src);
            blockingSelfLoops.add(src);
            selfLoops.add(src);
            return;
        }
        if (activeNodes.has(tgt)) {
            directBlockers.get(src).add(tgt);
            directDependents.get(tgt).add(src);
        } else {
            const extNode = allNodes.get(tgt);
            if (!extNode || isActiveNodeStatus(extNode.status)) {
                unresolvableBlockers.add(src);
                otherUnresolvableBlockers.add(src);
            }
        }
    });
    // Include exported n.blocked_by blockers (excluding parent-child parents whose blockers
    // are evaluated dynamically via openParents so closing a parent or its blocker matches readiness.go).
    activeNodes.forEach((n, id) => {
        const parents = openParents.get(id);
        if (Array.isArray(n.blocked_by)) {
            for (const bId of n.blocked_by) {
                if (parentChildEdges.has(id + '\0' + bId)) continue;
                if (bId === id) {
                    unresolvableBlockers.add(id);
                    blockingSelfLoops.add(id);
                    selfLoops.add(id);
                } else if (parents && parents.has(bId)) {
                    // Inherited via openParents; do not flatten parent itself into directBlockers.
                    continue;
                } else if (activeNodes.has(bId)) {
                    directBlockers.get(id).add(bId);
                    directDependents.get(bId).add(id);
                } else {
                    const extNode = allNodes.get(bId);
                    if (!extNode || isActiveNodeStatus(extNode.status)) {
                        unresolvableBlockers.add(id);
                        otherUnresolvableBlockers.add(id);
                    }
                }
            }
        }
    });
    const model = {
        allNodes,
        activeNodes,
        directBlockers,
        directDependents,
        openParents,
        openChildren,
        unresolvableBlockers,
        otherUnresolvableBlockers,
        blockingSelfLoops,
        parentChildSelfLoops,
        parentChildEdges,
        selfLoops,
    };
    _standaloneWhatIfCache = {
        nodesRef: DATA.nodes,
        linksRef: DATA.links,
        nodesLen: DATA.nodes.length,
        linksLen: DATA.links.length,
        model,
    };
    return model;
}
function createStandaloneDepsEvaluator(model, simClosed) {
    const memo = new Map(); // id -> boolean
    const visiting = new Set();
    function check(cur) {
        if (simClosed.has(cur)) return { ok: true, cyclic: false };
        if (memo.has(cur)) return { ok: memo.get(cur), cyclic: false };
        if (visiting.has(cur)) return { ok: false, cyclic: true };
        visiting.add(cur);
        let ok = true;
        let cyclic = false;
        if (model.unresolvableBlockers.has(cur)) {
            ok = false;
        } else {
            for (const bId of (model.directBlockers.get(cur) || [])) {
                if (!simClosed.has(bId)) {
                    ok = false;
                    break;
                }
            }
            if (ok) {
                for (const pId of (model.openParents.get(cur) || [])) {
                    if (simClosed.has(pId)) continue; // Closing parent stops inheritance (readiness.go)
                    const res = check(pId);
                    if (res.cyclic) cyclic = true;
                    if (!res.ok) {
                        ok = false;
                        break;
                    }
                }
            }
        }
        visiting.delete(cur);
        if (!cyclic) memo.set(cur, ok);
        return { ok, cyclic };
    }
    return id => check(id).ok;
}
function simulateWhatIfClose(targetOrIds) {
    const model = buildStandaloneWhatIfModel();
    const rawList = Array.isArray(targetOrIds) ? targetOrIds : [targetOrIds];
    const activeIds = [];
    const seenInput = new Set();
    for (const id of rawList) {
        if (!id || seenInput.has(id) || !model.activeNodes.has(id)) continue;
        seenInput.add(id);
        activeIds.push(id);
    }
    if (activeIds.length === 0) {
        return { direct: [], transitive: [], cascade: [], synergy: [] };
    }
    const baseClosed = new Set();
    const beforeDepsOk = createStandaloneDepsEvaluator(model, baseClosed);
    const beforeReady = new Set();
    model.activeNodes.forEach((n, id) => {
        if (isWhatIfUnlockableNode(n) && beforeDepsOk(id)) beforeReady.add(id);
    });
    const simClosed = new Set(activeIds);
    const afterDepsOk = createStandaloneDepsEvaluator(model, simClosed);
    const direct = [];
    model.activeNodes.forEach((n, id) => {
        if (!simClosed.has(id) && !beforeReady.has(id) && isWhatIfUnlockableNode(n) && afterDepsOk(id)) {
            direct.push(id);
        }
    });
    direct.sort();
    // W31-2: Identify joint diamond-dependency unlocks that only appear when the full batch closes.
    const synergy = [];
    if (activeIds.length > 1 && direct.length > 0) {
        const singleUnion = new Set();
        for (const singleId of activeIds) {
            const singleDepsOk = createStandaloneDepsEvaluator(model, new Set([singleId]));
            for (const dId of direct) {
                if (singleId !== dId && singleDepsOk(dId)) singleUnion.add(dId);
            }
        }
        for (const dId of direct) {
            if (!singleUnion.has(dId)) synergy.push(dId);
        }
    }
    const visited = new Set(direct);
    const cascade = [...direct];
    const queue = [...direct];
    while (queue.length > 0) {
        const v = queue.shift();
        simClosed.add(v);
        const candSeen = new Set();
        const expandQueue = [];
        const candidates = [];
        const enqueueCandidate = x => {
            if (candSeen.has(x)) return;
            candSeen.add(x);
            candidates.push(x);
            if (!simClosed.has(x)) expandQueue.push(x);
        };
        for (const d of (model.directDependents.get(v) || [])) enqueueCandidate(d);
        for (const c of (model.openChildren.get(v) || [])) enqueueCandidate(c);
        while (expandQueue.length > 0) {
            const p = expandQueue.shift();
            for (const c of (model.openChildren.get(p) || [])) enqueueCandidate(c);
        }
        if (candidates.length === 0) continue;
        candidates.sort();
        const stepDepsOk = createStandaloneDepsEvaluator(model, simClosed);
        for (const w of candidates) {
            if (!visited.has(w) && !simClosed.has(w) && isWhatIfUnlockableNode(model.activeNodes.get(w)) && stepDepsOk(w)) {
                visited.add(w);
                cascade.push(w);
                queue.push(w);
            }
        }
    }
    return { direct, transitive: cascade, cascade, synergy };
}
function computeStandaloneRootBlockers(targetId) {
    const model = buildStandaloneWhatIfModel();
    const targetNode = model.activeNodes.get(targetId);
    if (!targetNode) return null;
    const depsOk = createStandaloneDepsEvaluator(model, new Set());
    const openBlockersOf = id => {
        const out = [];
        const seen = new Set();
        for (const bId of (model.directBlockers.get(id) || [])) {
            if (!seen.has(bId)) { seen.add(bId); out.push(bId); }
        }
        for (const pId of (model.openParents.get(id) || [])) {
            if (!depsOk(pId) && !seen.has(pId)) { seen.add(pId); out.push(pId); }
        }
        return out;
    };
    const firstHop = openBlockersOf(targetId);
    if (firstHop.length === 0 && !model.selfLoops.has(targetId)) return null;
    const minDepth = new Map();
    const queue = [];
    for (const b of firstHop) {
        if (b === targetId) continue;
        if (!minDepth.has(b)) {
            minDepth.set(b, 1);
            queue.push(b);
        }
    }
    const roots = [];
    const seenRoot = new Set();
    let maxDepth = 0;
    for (let head = 0; head < queue.length; head++) {
        const u = queue[head];
        const d = minDepth.get(u);
        if (d > maxDepth) maxDepth = d;
        const next = openBlockersOf(u);
        if (next.length === 0 && !model.unresolvableBlockers.has(u)) {
            if (!seenRoot.has(u)) {
                seenRoot.add(u);
                const n = model.activeNodes.get(u);
                roots.push({
                    id: u,
                    title: n?.title || u,
                    status: n?.status || 'open',
                    priority: typeof n?.priority === 'number' ? n.priority : 2,
                    depth: d,
                    actionable: isWhatIfUnlockableNode(n),
                });
            }
        } else {
            for (const nxt of next) {
                if (nxt !== targetId && !minDepth.has(nxt)) {
                    minDepth.set(nxt, d + 1);
                    queue.push(nxt);
                }
            }
        }
    }
    // Cycle check across reachable upstream blockers
    let hasCycle = firstHop.includes(targetId) || model.selfLoops.has(targetId);
    if (!hasCycle) {
        const state = new Map(); // 1=visiting, 2=done
        const dfs = u => {
            state.set(u, 1);
            if (model.selfLoops.has(u)) {
                hasCycle = true;
                return;
            }
            for (const nxt of openBlockersOf(u)) {
                if (nxt === targetId || state.get(nxt) === 1) {
                    hasCycle = true;
                    return;
                }
                if (!state.has(nxt) && minDepth.has(nxt)) {
                    dfs(nxt);
                    if (hasCycle) return;
                }
            }
            state.set(u, 2);
        };
        for (const b of firstHop) {
            if (!state.has(b) && minDepth.has(b)) dfs(b);
            if (hasCycle) break;
        }
    }
    roots.sort((a, b) =>
        (Number(b.actionable) - Number(a.actionable)) ||
        (a.priority - b.priority) ||
        (a.depth - b.depth) ||
        String(a.id).localeCompare(String(b.id))
    );
    return { isBlocked: true, chainLength: minDepth.size, maxDepth, roots, hasCycle };
}
let activeWhatIfIds = [];
function clearWhatIfSimulation() {
    activeWhatIfId = null;
    activeWhatIfIds = [];
    const banner = document.getElementById('whatif-banner');
    if (banner) { banner.style.display = 'none'; banner.classList.remove('visible'); }
}
function runWhatIfBatchSimulation(nodeIds) {
    const validIds = [];
    const seen = new Set();
    for (const rawId of (Array.isArray(nodeIds) ? nodeIds : [nodeIds])) {
        if (!rawId || seen.has(rawId)) continue;
        const targetNode = lookupStandaloneNode(rawId);
        if (!targetNode || !isActiveNodeStatus(targetNode.status)) continue;
        seen.add(rawId);
        validIds.push(rawId);
    }
    if (validIds.length === 0) {
        clearWhatIfSimulation();
        activePathNodes = null;
        highlightedNodes = new Set();
        Graph.nodeColor(Graph.nodeColor());
        Graph.linkColor(Graph.linkColor());
        Graph.linkWidth(Graph.linkWidth());
        return null;
    }
    cancelPathFinder();
    cancelCheckDep();
    closeCycleInspector();
    const res = simulateWhatIfClose(validIds);
    activeWhatIfIds = validIds;
    activeWhatIfId = validIds[0];
    const ids = new Set([...validIds, ...res.cascade]);
    activePathNodes = ids;
    highlightedNodes = ids;
    Graph.nodeColor(Graph.nodeColor());
    Graph.linkColor(Graph.linkColor());
    Graph.linkWidth(Graph.linkWidth());
    const label = validIds.length === 1
        ? validIds[0]
        : '[' + validIds.slice(0, 3).join(' + ') + (validIds.length > 3 ? ' +' + (validIds.length - 3) : '') + ']';
    const synergyTag = res.synergy && res.synergy.length > 0 ? ' (+' + res.synergy.length + ' synergy)' : '';
    const banner = document.getElementById('whatif-banner');
    const textEl = document.getElementById('whatif-banner-text');
    if (banner && textEl) {
        textEl.textContent = '⚡ What-If ' + label + ': +' + res.direct.length + ' direct' + synergyTag + ' / +' + res.transitive.length + ' total unblocked' +
            (res.cascade.length ? ' (' + res.cascade.slice(0, 5).join(', ') + (res.cascade.length > 5 ? ' +' + (res.cascade.length - 5) : '') + ')' : '');
        banner.style.display = 'flex';
        banner.classList.add('visible');
    }
    showToast('What-If ' + label + ': +' + res.direct.length + ' direct' + synergyTag + ', +' + res.transitive.length + ' total unblocked');
    return res;
}
function runWhatIfSimulation(nodeId) {
    return runWhatIfBatchSimulation(nodeId ? [nodeId] : []);
}
window.bvRunStandaloneWhatIfBatch = runWhatIfBatchSimulation;
window.bvSimulateWhatIfClose = simulateWhatIfClose;
document.getElementById('btn-whatif-close').onclick = () => {
    clearWhatIfSimulation();
    closeCycleInspector();
    activePathNodes = null;
    highlightedNodes = new Set();
    Graph.nodeColor(Graph.nodeColor());
    Graph.linkColor(Graph.linkColor());
    Graph.linkWidth(Graph.linkWidth());
};
document.getElementById('btn-detail-whatif').onclick = () => {
    if (selectedNode && isActiveNodeStatus(selectedNode.status)) runWhatIfSimulation(selectedNode.id);
};

// Pre-flight Dependency Validator ("Check Add Dep A → B", W30-4)
let checkDepMode = false;
let checkDepFromId = null;
let checkDepToId = null;
let checkDepCommand = '';
function quoteStandaloneWord(word) {
    const s = String(word || '');
    if (/^[A-Za-z0-9_./:@+-]+$/.test(s)) return s;
    return "'" + s.replace(/'/g, "'\"'\"'") + "'";
}
function cancelCheckDep() {
    checkDepMode = false;
    checkDepFromId = null;
    checkDepToId = null;
    checkDepCommand = '';
    const banner = document.getElementById('checkdep-banner');
    if (banner) { banner.style.display = 'none'; banner.classList.remove('visible'); }
    const copyBtn = document.getElementById('btn-checkdep-copy');
    if (copyBtn) copyBtn.style.display = 'none';
}
function checkStandaloneDep(fromId, toId) {
    const fromNode = lookupStandaloneNode(fromId);
    const toNode = lookupStandaloneNode(toId);
    const res = {
        fromId, toId,
        canAdd: false, wouldCycle: false, alreadyExists: false,
        wouldBlockActionable: false, crossRepo: false,
        cyclePath: [], warning: '', command: ''
    };
    if (!fromNode || !toNode) {
        res.warning = 'Unknown issue ID';
        return res;
    }
    const fromRepo = String(fromNode.source_repo || '').trim();
    const toRepo = String(toNode.source_repo || '').trim();
    res.crossRepo = Boolean(fromRepo && toRepo && fromRepo.toLowerCase() !== toRepo.toLowerCase());
    if (fromId === toId) {
        res.wouldCycle = true;
        res.cyclePath = [fromId, toId];
        res.warning = 'Self-dependency ' + fromId + ' → ' + toId + ' would create a 1-node cycle';
        return res;
    }
    const adj = new Map();
    DATA.links.forEach(l => {
        const s = typeof l.source === 'object' ? l.source.id : l.source;
        const t = typeof l.target === 'object' ? l.target.id : l.target;
        const lt = l.type || 'blocks';
        if (lt !== 'blocks' && lt !== 'conditional-blocks' && lt !== 'waits-for') return;
        if (!adj.has(s)) adj.set(s, new Set());
        adj.get(s).add(t);
    });
    if (adj.get(fromId) && adj.get(fromId).has(toId)) {
        res.canAdd = true;
        res.alreadyExists = true;
        res.warning = 'Blocking dependency ' + fromId + ' → ' + toId + ' already exists';
        return res;
    }
    const queue = [[toId]];
    const visited = new Set([toId]);
    let foundPath = null;
    while (queue.length > 0) {
        const path = queue.shift();
        const cur = path[path.length - 1];
        if (cur === fromId) { foundPath = path; break; }
        const nexts = adj.get(cur);
        if (!nexts) continue;
        nexts.forEach(nxt => {
            if (!visited.has(nxt)) {
                visited.add(nxt);
                queue.push([...path, nxt]);
            }
        });
    }
    if (foundPath) {
        res.wouldCycle = true;
        res.cyclePath = [fromId, ...foundPath];
        res.warning = 'Cycle detected: ' + res.cyclePath.join(' → ');
        return res;
    }
    if (isActiveNodeStatus(fromNode.status) && isActiveNodeStatus(toNode.status)) {
        const model = buildStandaloneWhatIfModel();
        const depsOk = createStandaloneDepsEvaluator(model, new Set());
        const readySet = new Set();
        model.activeNodes.forEach((n, id) => {
            if (isWhatIfUnlockableNode(n) && depsOk(id)) readySet.add(id);
        });
        const seen = new Set([fromId]);
        const descQ = [fromId];
        while (descQ.length > 0) {
            const cur = descQ.shift();
            if (readySet.has(cur)) {
                res.wouldBlockActionable = true;
                break;
            }
            const kids = model.openChildren.get(cur);
            if (kids) {
                kids.forEach(k => {
                    if (!seen.has(k)) { seen.add(k); descQ.push(k); }
                });
            }
        }
    }
    res.canAdd = true;
    const isSafeDepArg = (s) => typeof s === 'string' && s.length > 0 && !s.startsWith('-') && !/[\x00-\x1f\x7f-\x9f]/.test(s);
    const allowCmd = !(DATA.source_authority && DATA.source_authority.claim_safe === false) &&
        DATA.source_kind !== 'workspace' &&
        !DATA.as_of && !DATA.as_of_commit;
    if (allowCmd && !res.crossRepo && isSafeDepArg(fromId) && isSafeDepArg(toId)) {
        res.command = 'br dep add ' + quoteStandaloneWord(fromId) + ' ' + quoteStandaloneWord(toId);
    }
    return res;
}
function startStandaloneCheckDep(fromId) {
    if (!fromId) return;
    cancelPathFinder();
    closeCycleInspector();
    clearWhatIfSimulation();
    checkDepMode = true;
    checkDepFromId = fromId;
    checkDepToId = null;
    checkDepCommand = '';
    activePathNodes = new Set([fromId]);
    highlightedNodes = new Set([fromId]);
    Graph.nodeColor(Graph.nodeColor());
    Graph.linkColor(Graph.linkColor());
    Graph.linkWidth(Graph.linkWidth());
    const banner = document.getElementById('checkdep-banner');
    const textEl = document.getElementById('checkdep-banner-text');
    const copyBtn = document.getElementById('btn-checkdep-copy');
    if (copyBtn) copyBtn.style.display = 'none';
    if (banner && textEl) {
        banner.style.borderColor = '#6366f1';
        textEl.textContent = '🧪 Check Add Dep: ' + fromId + ' → click target blocker node (Esc to cancel)';
        banner.style.display = 'flex';
        banner.classList.add('visible');
    }
    showToast('Check Add Dep: click target node for ' + fromId);
}
function runStandaloneCheckDep(fromId, toId) {
    cancelPathFinder();
    closeCycleInspector();
    clearWhatIfSimulation();
    const res = checkStandaloneDep(fromId, toId);
    checkDepMode = false;
    checkDepFromId = fromId;
    checkDepToId = toId;
    checkDepCommand = res.command || '';
    if (res.wouldCycle && res.cyclePath.length) {
        const ids = new Set(res.cyclePath);
        activePathNodes = ids;
        highlightedNodes = ids;
    } else if (res.canAdd) {
        const ids = new Set([fromId, toId]);
        activePathNodes = ids;
        highlightedNodes = ids;
    } else {
        activePathNodes = null;
        highlightedNodes = new Set();
    }
    Graph.nodeColor(Graph.nodeColor());
    Graph.linkColor(Graph.linkColor());
    Graph.linkWidth(Graph.linkWidth());
    const banner = document.getElementById('checkdep-banner');
    const textEl = document.getElementById('checkdep-banner-text');
    const copyBtn = document.getElementById('btn-checkdep-copy');
    if (banner && textEl) {
        if (res.wouldCycle) {
            banner.style.borderColor = '#ef4444';
            textEl.textContent = '⛔ Cycle: ' + res.cyclePath.join(' → ');
        } else if (!res.canAdd) {
            banner.style.borderColor = '#f59e0b';
            textEl.textContent = '⚠ Invalid: ' + (res.warning || (fromId + ' → ' + toId));
        } else if (res.alreadyExists) {
            banner.style.borderColor = '#3b82f6';
            textEl.textContent = 'ℹ Already exists: ' + fromId + ' → ' + toId;
        } else {
            banner.style.borderColor = res.wouldBlockActionable ? '#f59e0b' : '#10b981';
            let msg = '✓ Safe: ' + fromId + ' → ' + toId;
            if (res.wouldBlockActionable) msg += ' (blocks ready work)';
            if (res.crossRepo) msg += ' (cross-repo)';
            textEl.textContent = msg;
        }
        banner.style.display = 'flex';
        banner.classList.add('visible');
    }
    if (copyBtn) {
        copyBtn.style.display = checkDepCommand ? 'inline-block' : 'none';
        copyBtn.title = checkDepCommand;
    }
    return res;
}
window.bvCheckStandaloneDep = checkStandaloneDep;
window.bvRunStandaloneCheckDep = runStandaloneCheckDep;

// Pure-JS active dependency removal / cycle-cut preflight matching --robot-check-dep remove:FROM:TO (W33-2 + P24B-2/P24B-3).
function countStandaloneActiveCycles(model, excludeFrom, excludeTo) {
    const ids = [...model.activeNodes.keys()].sort();
    const adj = new Map();
    const MAX_CYCLES = 100;
    const MAX_STEPS = 50000;
    let cycleCount = 0;

    // Count blocking self-loops directly (excluding the proposed removed edge).
    if (model.blockingSelfLoops) {
        model.blockingSelfLoops.forEach(id => {
            if (id === excludeFrom && id === excludeTo) return;
            if (model.activeNodes.has(id) && cycleCount < MAX_CYCLES) cycleCount++;
        });
    }
    if (cycleCount >= MAX_CYCLES) return { count: cycleCount, truncated: true };

    ids.forEach(id => {
        const nexts = [];
        const bs = model.directBlockers.get(id);
        if (bs) {
            bs.forEach(bId => {
                if (id === excludeFrom && bId === excludeTo) return;
                if (bId === id) return;
                if (model.activeNodes.has(bId)) nexts.push(bId);
            });
        }
        nexts.sort();
        adj.set(id, nexts);
    });

    // Tarjan SCC decomposition (O(V+E)) eliminates exponential blowup on acyclic DAGs
    // and guarantees any cycle of arbitrary length (>25 nodes) is never falsely counted as 0.
    let tarjanIdx = 0;
    const indexMap = new Map();
    const lowlink = new Map();
    const onStack = new Set();
    const tStack = [];
    const sccs = [];
    const strongConnect = (v) => {
        indexMap.set(v, tarjanIdx);
        lowlink.set(v, tarjanIdx);
        tarjanIdx++;
        tStack.push(v);
        onStack.add(v);
        for (const w of (adj.get(v) || [])) {
            if (!indexMap.has(w)) {
                strongConnect(w);
                lowlink.set(v, Math.min(lowlink.get(v), lowlink.get(w)));
            } else if (onStack.has(w)) {
                lowlink.set(v, Math.min(lowlink.get(v), indexMap.get(w)));
            }
        }
        if (lowlink.get(v) === indexMap.get(v)) {
            const comp = [];
            while (true) {
                const w = tStack.pop();
                onStack.delete(w);
                comp.push(w);
                if (w === v) break;
            }
            if (comp.length > 1) {
                comp.sort();
                sccs.push(comp);
            }
        }
    };
    for (const id of ids) {
        if (!indexMap.has(id)) strongConnect(id);
    }

    // Bounded Johnson elementary cycle enumeration inside each non-trivial SCC.
    let steps = 0;
    for (const comp of sccs) {
        if (cycleCount >= MAX_CYCLES) break;
        const compSet = new Set(comp);
        const beforeComp = cycleCount;
        const idxInComp = new Map(comp.map((id, i) => [id, i]));
        for (let s = 0; s < comp.length && cycleCount < MAX_CYCLES && steps < MAX_STEPS; s++) {
            const startId = comp[s];
            const blocked = new Set();
            const blockMap = new Map();
            const unblock = (u) => {
                blocked.delete(u);
                const bs = blockMap.get(u);
                if (bs) {
                    for (const w of [...bs]) {
                        bs.delete(w);
                        if (blocked.has(w)) unblock(w);
                    }
                }
            };
            const circuit = (v) => {
                if (cycleCount >= MAX_CYCLES || steps >= MAX_STEPS) return false;
                steps++;
                let found = false;
                blocked.add(v);
                for (const w of (adj.get(v) || [])) {
                    if (!compSet.has(w) || idxInComp.get(w) < s) continue;
                    if (w === startId) {
                        cycleCount++;
                        found = true;
                        if (cycleCount >= MAX_CYCLES) return true;
                    } else if (!blocked.has(w)) {
                        if (circuit(w)) found = true;
                    }
                }
                if (found) {
                    unblock(v);
                } else {
                    for (const w of (adj.get(v) || [])) {
                        if (!compSet.has(w) || idxInComp.get(w) < s) continue;
                        if (!blockMap.has(w)) blockMap.set(w, new Set());
                        blockMap.get(w).add(v);
                    }
                }
                return found;
            };
            circuit(startId);
        }
        // Any non-trivial SCC provably contains >=1 cycle even if step budget was hit.
        if (cycleCount === beforeComp) {
            cycleCount = Math.min(MAX_CYCLES, cycleCount + 1);
        }
    }
    return { count: cycleCount, truncated: cycleCount >= MAX_CYCLES || steps >= MAX_STEPS };
}
function checkStandaloneDepRemoval(fromIdRaw, toIdRaw) {
    const fromId = String(fromIdRaw || '').trim();
    const toId = String(toIdRaw || '').trim();
    const res = {
        operation: 'remove',
        fromId,
        toId,
        exists: false,
        canRemove: false,
        cyclesBefore: 0,
        cyclesAfter: 0,
        cyclesBroken: 0,
        cyclesTruncated: false,
        wouldBreakAllCycles: false,
        newlyUnblocked: [],
        crossRepo: false,
        warning: '',
        command: '',
    };
    if (!fromId || !toId) {
        res.warning = 'Both source and target issue IDs are required';
        return res;
    }
    const model = buildStandaloneWhatIfModel();
    const fromNode = model.allNodes.get(fromId);
    const toNode = model.allNodes.get(toId);
    if (!fromNode || !toNode) {
        const missing = [];
        if (!fromNode) missing.push(fromId);
        if (!toNode && toId !== fromId) missing.push(toId);
        res.warning = 'Unknown issue ID(s): ' + missing.join(', ');
        return res;
    }
    const fromRepo = String(fromNode.source_repo || '').trim();
    const toRepo = String(toNode.source_repo || '').trim();
    res.crossRepo = Boolean(fromRepo && toRepo && fromRepo.toLowerCase() !== toRepo.toLowerCase());

    let edgeExists = false;
    DATA.links.forEach(l => {
        if (edgeExists) return;
        const src = typeof l.source === 'object' ? l.source.id : l.source;
        const tgt = typeof l.target === 'object' ? l.target.id : l.target;
        if (src === fromId && tgt === toId && isBlockingLinkType(l.type || 'blocks')) {
            edgeExists = true;
        }
    });
    if (!edgeExists && Array.isArray(fromNode.blocked_by) && fromNode.blocked_by.includes(toId)) {
        const parents = model.openParents.get(fromId);
        const isParentChild = model.parentChildEdges && model.parentChildEdges.has(fromId + '\0' + toId);
        if (!isParentChild && (!parents || !parents.has(toId))) edgeExists = true;
    }
    if (!edgeExists) {
        res.warning = 'Blocking dependency ' + fromId + ' → ' + toId + ' does not exist';
        return res;
    }
    res.exists = true;
    res.canRemove = true;
    const beforeCycles = countStandaloneActiveCycles(model, null, null);
    const afterCycles = countStandaloneActiveCycles(model, fromId, toId);
    res.cyclesBefore = beforeCycles.count;
    res.cyclesAfter = afterCycles.count;
    res.cyclesTruncated = Boolean(beforeCycles.truncated || afterCycles.truncated);
    if (res.cyclesTruncated) {
        if (afterCycles.truncated || (res.cyclesBefore > 0 && res.cyclesAfter >= res.cyclesBefore)) {
            res.cyclesBroken = 0;
            res.wouldBreakAllCycles = false;
            res.warning = 'Cycle enumeration reached cap (100); exact cycle reduction is inconclusive';
        } else if (res.cyclesAfter === 0) {
            res.cyclesBroken = res.cyclesBefore;
            res.wouldBreakAllCycles = res.cyclesBefore > 0;
            res.warning = 'Cycle enumeration reached cap (100) before removal; removal leaves 0 active cycles (acyclic)';
        } else {
            res.cyclesBroken = Math.max(0, res.cyclesBefore - res.cyclesAfter);
            res.wouldBreakAllCycles = false;
            res.warning = 'Cycle enumeration reached cap (100); exact cycle reduction is approximate (≥ ' + res.cyclesBroken + ' broken, ' + res.cyclesAfter + ' remaining)';
        }
    } else {
        res.cyclesBroken = Math.max(0, res.cyclesBefore - res.cyclesAfter);
        res.wouldBreakAllCycles = res.cyclesBefore > 0 && res.cyclesAfter === 0;
    }

    // Evaluate newly unblocked issues by cloning directBlockers without fromId -> toId,
    // preserving unrelated unresolvableBlockers and parent-child self-loops (P24B-2).
    const emptyClosed = new Set();
    const beforeEval = createStandaloneDepsEvaluator(model, emptyClosed);
    const afterDirect = new Map();
    model.directBlockers.forEach((set, id) => {
        const copy = new Set(set);
        if (id === fromId) copy.delete(toId);
        afterDirect.set(id, copy);
    });
    const afterUnres = new Set(model.otherUnresolvableBlockers || []);
    if (model.parentChildSelfLoops) {
        model.parentChildSelfLoops.forEach(id => afterUnres.add(id));
    }
    if (model.blockingSelfLoops) {
        model.blockingSelfLoops.forEach(id => {
            if (fromId === toId && id === fromId) return;
            afterUnres.add(id);
        });
    }
    const afterModel = { ...model, directBlockers: afterDirect, unresolvableBlockers: afterUnres };
    const afterEval = createStandaloneDepsEvaluator(afterModel, emptyClosed);
    const newly = [];
    model.activeNodes.forEach((n, id) => {
        if (!isWhatIfUnlockableNode(n)) return;
        if (!beforeEval(id) && afterEval(id)) {
            newly.push(id);
        }
    });
    newly.sort();
    res.newlyUnblocked = newly;

    const isSafeDepArg = (s) => typeof s === 'string' && s.length > 0 && !s.startsWith('-') && !/[\x00-\x1f\x7f-\x9f]/.test(s);
    const allowCmd = !(DATA.source_authority && DATA.source_authority.claim_safe === false) &&
        DATA.source_kind !== 'workspace' &&
        !DATA.as_of && !DATA.as_of_commit;
    if (allowCmd && !res.crossRepo && isSafeDepArg(fromId) && isSafeDepArg(toId)) {
        res.command = 'br dep remove ' + quoteStandaloneWord(fromId) + ' ' + quoteStandaloneWord(toId);
    }
    return res;
}
function runStandaloneCheckDepRemoval(fromId, toId) {
    cancelPathFinder();
    closeCycleInspector();
    clearWhatIfSimulation();
    const res = checkStandaloneDepRemoval(fromId, toId);
    checkDepMode = false;
    checkDepFromId = fromId;
    checkDepToId = toId;
    checkDepCommand = res.command || '';
    if (res.exists) {
        const ids = new Set([fromId, toId, ...(res.newlyUnblocked || [])]);
        activePathNodes = ids;
        highlightedNodes = ids;
    } else {
        activePathNodes = null;
        highlightedNodes = new Set();
    }
    Graph.nodeColor(Graph.nodeColor());
    Graph.linkColor(Graph.linkColor());
    Graph.linkWidth(Graph.linkWidth());
    const banner = document.getElementById('checkdep-banner');
    const textEl = document.getElementById('checkdep-banner-text');
    const copyBtn = document.getElementById('btn-checkdep-copy');
    if (banner && textEl) {
        if (!res.exists) {
            banner.style.borderColor = '#f59e0b';
            textEl.textContent = '⚠ Missing: ' + (res.warning || (fromId + ' → ' + toId));
        } else {
            banner.style.borderColor = res.cyclesTruncated ? '#f59e0b' : '#10b981';
            let msg = '✂ Remove ' + fromId + ' → ' + toId;
            if (res.cyclesTruncated && res.warning) {
                msg += ' · ⚠ ' + res.warning;
            } else if (res.cyclesBefore > 0) {
                msg += res.wouldBreakAllCycles
                    ? (' · breaks ' + res.cyclesBroken + '/' + res.cyclesBefore + ' → acyclic')
                    : (' · breaks ' + res.cyclesBroken + '/' + res.cyclesBefore + ' cycles');
            }
            if (res.newlyUnblocked.length > 0) {
                msg += ' · +' + res.newlyUnblocked.length + ' ready (' + res.newlyUnblocked.slice(0, 3).join(', ') + (res.newlyUnblocked.length > 3 ? ' +' + (res.newlyUnblocked.length - 3) : '') + ')';
            }
            textEl.textContent = msg;
        }
        banner.style.display = 'flex';
        banner.classList.add('visible');
    }
    if (copyBtn) {
        copyBtn.style.display = checkDepCommand ? 'inline-block' : 'none';
        copyBtn.title = checkDepCommand;
    }
    return res;
}
window.bvCheckStandaloneDepRemoval = checkStandaloneDepRemoval;
window.bvRunStandaloneCheckDepRemoval = runStandaloneCheckDepRemoval;
document.getElementById('btn-checkdep-copy').onclick = () => {
    if (checkDepCommand) void copyContextField(checkDepCommand, 'Command');
};
document.getElementById('btn-checkdep-close').onclick = () => {
    cancelCheckDep();
    activePathNodes = null;
    highlightedNodes = new Set();
    Graph.nodeColor(Graph.nodeColor());
    Graph.linkColor(Graph.linkColor());
    Graph.linkWidth(Graph.linkWidth());
};

// Cycle Inspector (uses exported DATA.cycles + DATA.cycle_breaks)
let activeCycleIndex = -1;
let activeCycleBreakEdge = null;
function closeCycleInspector() {
    activeCycleIndex = -1;
    activeCycleBreakEdge = null;
    const banner = document.getElementById('cycle-banner');
    if (banner) { banner.style.display = 'none'; banner.classList.remove('visible'); }
    const previewBtn = document.getElementById('btn-cycle-preview-cut');
    if (previewBtn) previewBtn.style.display = 'none';
    const btn = document.getElementById('btn-cycles');
    if (btn) btn.classList.remove('active');
}
function showCycleInspector(idx, step = 1) {
    const cycles = Array.isArray(DATA.cycles) ? DATA.cycles : [];
    if (cycles.length === 0) return;
    const graphNodeById = new Map((Graph.graphData().nodes || []).map(n => [n.id, n]));
    const dir = step < 0 ? -1 : 1;
    const startIdx = ((idx %% cycles.length) + cycles.length) %% cycles.length;
    let chosenIdx = -1;
    let openCycle = [];
    for (let offset = 0; offset < cycles.length; offset++) {
        const candIdx = (((startIdx + offset * dir) %% cycles.length) + cycles.length) %% cycles.length;
        const rawCycle = cycles[candIdx] || [];
        const candOpen = (rawCycle.length > 1 && rawCycle[0] === rawCycle[rawCycle.length - 1])
            ? rawCycle.slice(0, -1)
            : rawCycle;
        if (candOpen.length > 0 && candOpen.some(id => {
            const n = graphNodeById.get(id);
            return n && currentVisibilityFilter(n);
        })) {
            chosenIdx = candIdx;
            openCycle = candOpen;
            break;
        }
    }
    if (chosenIdx < 0 || openCycle.length === 0) {
        showToast('Cycle nodes are hidden by active filters');
        return;
    }
    cancelPathFinder();
    cancelCheckDep();
    clearWhatIfSimulation();
    activeCycleIndex = chosenIdx;
    const nodeSet = new Set(openCycle);
    activePathNodes = nodeSet;
    highlightedNodes = nodeSet;
    Graph.nodeColor(Graph.nodeColor());
    Graph.linkColor(Graph.linkColor());
    Graph.linkWidth(Graph.linkWidth());
    const cycleEdges = new Set();
    for (let i = 0; i < openCycle.length; i++) {
        cycleEdges.add(openCycle[i] + '\0' + openCycle[(i + 1) %% openCycle.length]);
    }
    const matchingBreaks = (DATA.cycle_breaks || []).filter(b => b && cycleEdges.has(b.from + '\0' + b.to));
    matchingBreaks.sort((a, b) => {
        const cbDiff = (Number(b.cycles_broken) || 0) - (Number(a.cycles_broken) || 0);
        if (cbDiff !== 0) return cbDiff;
        const aCol = typeof a.collateral === 'number' ? a.collateral : Number.MAX_SAFE_INTEGER;
        const bCol = typeof b.collateral === 'number' ? b.collateral : Number.MAX_SAFE_INTEGER;
        if (aCol !== bCol) return aCol - bCol;
        return String(a.from || '').localeCompare(String(b.from || '')) || String(a.to || '').localeCompare(String(b.to || ''));
    });
    const breakSug = matchingBreaks[0] || null;
    activeCycleBreakEdge = breakSug;
    const banner = document.getElementById('cycle-banner');
    const textEl = document.getElementById('cycle-banner-text');
    const previewCutBtn = document.getElementById('btn-cycle-preview-cut');
    if (previewCutBtn) {
        previewCutBtn.style.display = (breakSug && breakSug.from && breakSug.to) ? 'inline-block' : 'none';
    }
    if (banner && textEl) {
        const pathStr = openCycle.concat([openCycle[0]]).join(' → ');
        let cutSuffix = '';
        if (breakSug) {
            cutSuffix = ' · Cut: ' + breakSug.from + ' → ' + breakSug.to;
            if (typeof breakSug.collateral === 'number') {
                cutSuffix += ' (collateral: ' + breakSug.collateral + ')';
            }
        }
        textEl.textContent = '🔄 Cycle [' + (activeCycleIndex + 1) + '/' + cycles.length + ']: ' + pathStr + cutSuffix;
        if (breakSug && breakSug.rationale) {
            textEl.title = breakSug.rationale;
        } else {
            textEl.removeAttribute('title');
        }
        banner.style.display = 'flex';
        banner.classList.add('visible');
    }
    const btn = document.getElementById('btn-cycles');
    if (btn) btn.classList.add('active');
}
const cyclesBtn = document.getElementById('btn-cycles');
if (cyclesBtn && Array.isArray(DATA.cycles) && DATA.cycles.length > 0) {
    cyclesBtn.style.display = '';
    cyclesBtn.title = 'Inspect ' + DATA.cycles.length + ' dependency cycle(s) (C)';
    cyclesBtn.onclick = () => {
        if (activeCycleIndex >= 0) {
            closeCycleInspector();
            activePathNodes = null;
            highlightedNodes = new Set();
            Graph.nodeColor(Graph.nodeColor());
            Graph.linkColor(Graph.linkColor());
            Graph.linkWidth(Graph.linkWidth());
        } else {
            showCycleInspector(0);
        }
    };
}
document.getElementById('btn-cycle-preview-cut').onclick = () => {
    if (activeCycleBreakEdge && activeCycleBreakEdge.from && activeCycleBreakEdge.to) {
        runStandaloneCheckDepRemoval(activeCycleBreakEdge.from, activeCycleBreakEdge.to);
    }
};
document.getElementById('btn-cycle-prev').onclick = () => { if (activeCycleIndex >= 0) showCycleInspector(activeCycleIndex - 1, -1); };
document.getElementById('btn-cycle-next').onclick = () => { if (activeCycleIndex >= 0) showCycleInspector(activeCycleIndex + 1, 1); };
document.getElementById('btn-cycle-close').onclick = () => {
    closeCycleInspector();
    clearWhatIfSimulation();
    activePathNodes = null;
    highlightedNodes = new Set();
    Graph.nodeColor(Graph.nodeColor());
    Graph.linkColor(Graph.linkColor());
    Graph.linkWidth(Graph.linkWidth());
};
document.getElementById('ctx-copy').onclick = () => {
    const value = contextNode ? contextNode.id : '';
    hideContextMenu();
    void copyContextField(value, 'ID');
};
document.getElementById('ctx-copy-description').onclick = () => {
    const value = contextNode ? contextNode.description : '';
    hideContextMenu();
    void copyContextField(value, 'Description');
};
document.getElementById('ctx-path').onclick = () => {
    if (contextNode) {
        closeCycleInspector();
        cancelCheckDep();
        clearWhatIfSimulation();
        pathFinderMode = true;
        pathStart = contextNode.id;
        activePathNodes = new Set([contextNode.id]);
        highlightedNodes = new Set([contextNode.id]);
        Graph.nodeColor(Graph.nodeColor());
        Graph.linkColor(Graph.linkColor());
        Graph.linkWidth(Graph.linkWidth());
        document.getElementById('btn-path').classList.add('active');
        updatePathfinderBanner('🔗 Path Finder: Start ' + contextNode.id + ' — click destination node (Esc to cancel)');
        showToast('Start node: ' + contextNode.id + ' — click destination node');
    }
    hideContextMenu();
};

function highlightPath(path) {
    activePathNodes = new Set(path);
    highlightedNodes = new Set(path);
    Graph.nodeColor(Graph.nodeColor());
    Graph.linkColor(Graph.linkColor());
    Graph.linkWidth(Graph.linkWidth());
    updateVisibleCount();
    showToast('Path (' + path.length + ' nodes): ' + path.join(' → '));
}

function handleNodeClick(d, event) {
    if (!d) return;
    if (checkDepMode && checkDepFromId) {
        runStandaloneCheckDep(checkDepFromId, d.id);
        return;
    }
    if (event && event.shiftKey) {
        if (!isActiveNodeStatus(d.status)) {
            showToast('What-If only simulates closing active issues');
            return;
        }
        const idx = activeWhatIfIds.indexOf(d.id);
        const nextIds = idx >= 0
            ? activeWhatIfIds.filter(id => id !== d.id)
            : [...activeWhatIfIds, d.id];
        runWhatIfBatchSimulation(nextIds);
        return;
    }
    if (pathFinderMode) {
        if (!pathStart) {
            pathStart = d.id;
            activePathNodes = new Set([d.id]);
            highlightedNodes = new Set([d.id]);
            Graph.nodeColor(Graph.nodeColor());
            Graph.linkColor(Graph.linkColor());
            Graph.linkWidth(Graph.linkWidth());
            updatePathfinderBanner('🔗 Path Finder: Start ' + d.id + ' — click destination node (Esc to cancel)');
            showToast('Start node: ' + d.id + ' — click destination node');
            return;
        }
        if (pathStart === d.id) {
            showToast('Select a different destination node');
            return;
        }
        const startId = pathStart;
        const path = bfsPath(startId, d.id);
        if (path && path.length > 0) {
            highlightPath(path);
            updatePathfinderBanner('🔗 Path (' + path.length + ' nodes): ' + path.join(' → ') + ' (click new start or Esc to exit)');
        } else {
            activePathNodes = null;
            highlightedNodes = new Set();
            Graph.nodeColor(Graph.nodeColor());
            Graph.linkColor(Graph.linkColor());
            Graph.linkWidth(Graph.linkWidth());
            updatePathfinderBanner('🔗 No path between ' + startId + ' and ' + d.id + ' — click start node (Esc to cancel)');
            showToast('No path found between ' + startId + ' and ' + d.id);
        }
        pathStart = null;
        return;
    }
    selectNode(d);
}

function highlightDependencies(node, type) {
    const connected = new Set([node.id]);
    DATA.links.forEach(l => {
        const src = typeof l.source === 'object' ? l.source.id : l.source;
        const tgt = typeof l.target === 'object' ? l.target.id : l.target;
        if (type === 'deps' && src === node.id) connected.add(tgt);
        if (type === 'dependents' && tgt === node.id) connected.add(src);
    });
    activePathNodes = connected;
    highlightedNodes = connected;
    Graph.nodeColor(Graph.nodeColor());
    Graph.linkColor(Graph.linkColor());
    Graph.linkWidth(Graph.linkWidth());
    updateVisibleCount();
    showToast(connected.size + ' nodes shown');
}

// Unified filter state and handler
let statusFilter = 'all', typeFilter = 'all', priorityFilter = 'all', labelFilter = 'all', repoFilter = 'all';
let diffOnlyFilter = false;
let currentVisibilityFilter = () => true;

// Wire optional --diff-since overlay HUD & "Changed only" filter (W32-3)
(function initDiffOverlay() {
    const ds = DATA.diff_summary;
    if (!ds) return;
    const banner = document.getElementById('diff-banner');
    const textEl = document.getElementById('diff-banner-text');
    const toolbarBtn = document.getElementById('btn-diff-only');
    const bannerBtn = document.getElementById('btn-diff-filter-toggle');
    const parts = [
        'Δ Since ' + (ds.diff_since || 'baseline') + ':',
        '+' + (ds.issues_added || 0) + ' added',
        '✓' + (ds.issues_closed || 0) + ' closed',
    ];
    if (ds.issues_reopened) parts.push('↺' + ds.issues_reopened + ' reopened');
    if (ds.issues_modified) parts.push('~' + ds.issues_modified + ' modified');
    if (ds.issues_removed) parts.push('-' + ds.issues_removed + ' removed');
    if (ds.cycle_delta) parts.push((ds.cycle_delta > 0 ? '+' : '') + ds.cycle_delta + ' cycles');
    if (ds.health_trend) parts.push('trend: ' + ds.health_trend);
    if (Array.isArray(ds.removed_ids) && ds.removed_ids.length > 0) {
        const shown = ds.removed_ids.slice(0, 3).join(', ');
        const more = ds.removed_ids.length > 3 ? (' +' + (ds.removed_ids.length - 3)) : '';
        parts.push('removed: ' + shown + more);
    }
    if (textEl) textEl.textContent = parts.join(' · ');
    if (banner) banner.style.display = 'flex';
    if (toolbarBtn) toolbarBtn.style.display = '';
    const toggleDiffOnly = () => {
        diffOnlyFilter = !diffOnlyFilter;
        if (toolbarBtn) toolbarBtn.classList.toggle('active', diffOnlyFilter);
        if (bannerBtn) bannerBtn.textContent = diffOnlyFilter ? 'Show all' : 'Changed only';
        applyFilters();
    };
    if (toolbarBtn) toolbarBtn.onclick = toggleDiffOnly;
    if (bannerBtn) bannerBtn.onclick = toggleDiffOnly;
})();

// Surface optional git history correlation summary badge in overlay stats (W37-7)
(function initHistoryStatsOverlay() {
    const hs = DATA.history_stats;
    if (!hs || !hs.total_commits) return;
    const statEl = document.getElementById('stat-history');
    const valEl = document.getElementById('stat-history-value');
    if (!statEl || !valEl) return;
    const parts = ['📜 ' + hs.total_commits + ' commits'];
    if (hs.beads_with_commits) parts.push(hs.beads_with_commits + ' beads');
    if (typeof hs.avg_cycle_time_days === 'number' && Number.isFinite(hs.avg_cycle_time_days)) {
        parts.push(hs.avg_cycle_time_days.toFixed(1) + 'd avg cycle');
    }
    valEl.textContent = parts.join(' · ');
    const titleParts = [];
    if (hs.unique_authors) titleParts.push(hs.unique_authors + ' authors');
    if (typeof hs.avg_commits_per_bead === 'number' && Number.isFinite(hs.avg_commits_per_bead)) {
        titleParts.push(hs.avg_commits_per_bead.toFixed(1) + ' commits/bead');
    }
    if (titleParts.length > 0) statEl.title = 'Git correlation: ' + titleParts.join(' · ');
    statEl.style.display = 'flex';
})();

const allLabels = new Set();
const allRepos = new Set();
DATA.nodes.forEach(n => {
    (n.labels || []).forEach(l => allLabels.add(l));
    if (n.source_repo) allRepos.add(n.source_repo);
});
const labelSelect = document.getElementById('filter-label');
[...allLabels].sort().forEach(l => {
    const opt = document.createElement('option');
    opt.value = l; opt.textContent = l;
    labelSelect.appendChild(opt);
});
const repoSelect = document.getElementById('filter-repo');
if (repoSelect && (allRepos.size > 1 || (allRepos.size === 1 && DATA.nodes.some(n => !n.source_repo)))) {
    [...allRepos].sort().forEach(r => {
        const opt = document.createElement('option');
        opt.value = r; opt.textContent = r;
        repoSelect.appendChild(opt);
    });
    repoSelect.style.display = '';
}

document.getElementById('filter-status').onchange = e => { statusFilter = e.target.value || 'all'; applyFilters(); };
document.getElementById('filter-type').onchange = e => { typeFilter = e.target.value || 'all'; applyFilters(); };
document.getElementById('filter-priority').onchange = e => { priorityFilter = e.target.value || 'all'; applyFilters(); };
labelSelect.onchange = e => { labelFilter = e.target.value || 'all'; applyFilters(); };
if (repoSelect) repoSelect.onchange = e => { repoFilter = e.target.value || 'all'; applyFilters(); };

function applyFilters() {
    const q = (searchQuery || document.getElementById('search-input').value || '').trim().toLowerCase();
    currentVisibilityFilter = n => {
        const matchSearch = !q ||
            n.id.toLowerCase().includes(q) ||
            n.title.toLowerCase().includes(q) ||
            (n.description || '').toLowerCase().includes(q) ||
            (n.design || '').toLowerCase().includes(q) ||
            (n.notes || '').toLowerCase().includes(q) ||
            (n.acceptance_criteria || '').toLowerCase().includes(q) ||
            (n.labels || []).some(l => l.toLowerCase().includes(q)) ||
            (n.assignee || '').toLowerCase().includes(q) ||
            (n.source_repo || '').toLowerCase().includes(q) ||
            (n.created_by || '').toLowerCase().includes(q) ||
            (n.close_reason || '').toLowerCase().includes(q);
        if (!matchSearch) return false;

        if (statusFilter && statusFilter !== 'all') {
            if (statusFilter === 'actionable') {
                if (!isNodeActionable(n)) return false;
            } else if (statusFilter === 'blocked') {
                if (!isNodeBlocked(n)) return false;
            } else if (n.status !== statusFilter) {
                return false;
            }
        }

        if (typeFilter && typeFilter !== 'all' && n.type !== typeFilter) return false;
        if (priorityFilter && priorityFilter !== 'all' && n.priority !== parseInt(priorityFilter, 10)) return false;
        if (labelFilter && labelFilter !== 'all' && !(n.labels || []).includes(labelFilter)) return false;
        if (repoFilter && repoFilter !== 'all' && (n.source_repo || '') !== repoFilter) return false;
        if (diffOnlyFilter && !n.diff_status) return false;
        return true;
    };
    Graph.nodeVisibility(currentVisibilityFilter);
    const visibleIds = new Set(DATA.nodes.filter(currentVisibilityFilter).map(n => n.id));
    let hidActivePanel = false;
    let needsHighlightRefresh = false;
    if (selectedNode && !visibleIds.has(selectedNode.id)) {
        selectedNode = null;
        document.getElementById('node-detail').classList.remove('visible');
        document.getElementById('no-selection').style.display = 'block';
        hidActivePanel = true;
    }
    if (hoveredNode && !visibleIds.has(hoveredNode.id)) {
        hoveredNode = null;
        hidActivePanel = true;
        if (!activePathNodes && highlightedNodes.size > 0) {
            highlightedNodes = new Set();
            needsHighlightRefresh = true;
        }
    }
    if (contextNode && !visibleIds.has(contextNode.id)) {
        hideContextMenu();
    }
    if (hidActivePanel || (activePanelNode && !visibleIds.has(activePanelNode.id))) {
        if (selectedNode) {
            showHoverPanel(selectedNode);
        } else {
            hideHoverPanel();
        }
    }
    if (pathStart && !visibleIds.has(pathStart)) {
        cancelPathFinder();
    }
    if ((checkDepMode || checkDepFromId || checkDepToId) &&
        ((checkDepFromId && !visibleIds.has(checkDepFromId)) || (checkDepToId && !visibleIds.has(checkDepToId)))) {
        cancelCheckDep();
        activePathNodes = null;
        highlightedNodes = new Set();
        needsHighlightRefresh = true;
    }
    if (highlightedNodes.size > 0 && ![...highlightedNodes].some(id => visibleIds.has(id))) {
        cancelPathFinder();
        cancelCheckDep();
        closeCycleInspector();
        clearWhatIfSimulation();
        activePathNodes = null;
        highlightedNodes = new Set();
        needsHighlightRefresh = true;
    }
    if (needsHighlightRefresh) {
        Graph.nodeColor(computeNodeColor).linkColor(Graph.linkColor()).linkWidth(Graph.linkWidth());
    }
    updateVisibleCount();
}

function updateVisibleCount() {
    const count = DATA.nodes.filter(n => currentVisibilityFilter(n)).length;
    document.getElementById('stat-visible').innerHTML = '<span class="stat-value">' + escapeHTML(count) + '</span> visible';
}

// View mode
document.getElementById('view-mode').onchange = e => {
    const mode = e.target.value;
    if (applyGraphDagMode(mode)) {
        setTimeout(() => Graph.zoomToFit(400), 100);
    }
};

// Size metric
const SIZE_METRIC_LABELS = { pagerank: 'PageRank', betweenness: 'Betweenness', critical: 'Critical Path', indegree: 'In-Degree', risk: 'Risk' };
document.getElementById('size-by').onchange = e => {
    sizeMetric = e.target.value;
    document.getElementById('heatmap-metric').textContent = SIZE_METRIC_LABELS[sizeMetric] || 'PageRank';
    Graph.nodeVal(n => getNodeSize(n));
    if (heatmapMode) Graph.nodeColor(computeNodeColor);
};

// Controls
document.getElementById('btn-fit').onclick = () => Graph.zoomToFit(400, 50);
document.getElementById('btn-reset').onclick = () => {
    document.getElementById('filter-status').value = 'all';
    document.getElementById('filter-type').value = 'all';
    document.getElementById('filter-priority').value = 'all';
    document.getElementById('filter-label').value = 'all';
    if (repoSelect) repoSelect.value = 'all';
    document.getElementById('search-input').value = '';
    document.getElementById('search-results').classList.remove('visible');
    document.getElementById('view-mode').value = 'force';
    document.getElementById('size-by').value = 'pagerank';
    searchQuery = '';
    statusFilter = 'all';
    typeFilter = 'all';
    priorityFilter = 'all';
    labelFilter = 'all';
    repoFilter = 'all';
    diffOnlyFilter = false;
    const diffBtn = document.getElementById('btn-diff-only');
    if (diffBtn) diffBtn.classList.remove('active');
    const diffToggleBtn = document.getElementById('btn-diff-filter-toggle');
    if (diffToggleBtn) diffToggleBtn.textContent = 'Changed only';
    sizeMetric = 'pagerank';
    document.getElementById('heatmap-metric').textContent = 'PageRank';
    heatmapMode = false;
    pathFinderMode = false;
    pathStart = null;
    activePathNodes = null;
    highlightedNodes = new Set();
    closeCycleInspector();
    cancelCheckDep();
    clearWhatIfSimulation();
    Graph.dagMode(null);
    setGraphLinksMode(false);
    Graph.nodeVal(n => getNodeSize(n));
    Graph.nodeColor(computeNodeColor);
    Graph.linkColor(computeLinkColor);
    Graph.linkDirectionalArrowColor(computeLinkArrowColor);
    Graph.linkWidth(Graph.linkWidth());
    applyFilters();
    clearSelection();
    hideContextMenu();
    hideHoverPanel();
    Graph.zoomToFit(400, 50);
    document.getElementById('heatmap-legend').classList.remove('heatmap-active');
    document.getElementById('top-nodes-panel').classList.remove('visible');
    document.getElementById('recent-panel').classList.remove('visible');
    document.getElementById('triage-panel').style.display = 'none';
    document.getElementById('pathfinder-banner').classList.remove('visible');
    document.getElementById('btn-heatmap').classList.remove('active');
    document.getElementById('btn-triage').classList.remove('active');
    document.getElementById('btn-top').classList.remove('active');
    document.getElementById('btn-recent').classList.remove('active');
    document.getElementById('btn-path').classList.remove('active');
};

// Heatmap toggle - legend always visible, toggle controls coloring mode
document.getElementById('btn-heatmap').onclick = () => {
    heatmapMode = !heatmapMode;
    document.getElementById('btn-heatmap').classList.toggle('active', heatmapMode);
    document.getElementById('heatmap-legend').classList.toggle('heatmap-active', heatmapMode);
    Graph.nodeColor(computeNodeColor);
};

// Triage panel (supports Top Recommendations, Quick Wins, Blockers to Clear, and Alerts)
let activeTriageTab = 'recommendations';
function renderTriageTab(tab) {
    activeTriageTab = tab || 'recommendations';
    document.querySelectorAll('.triage-tab-btn').forEach(b => {
        b.classList.toggle('active', b.dataset.tab === activeTriageTab);
    });
    const list = document.getElementById('triage-list');
    if (!list) return;
    const t = DATA.triage || {};
    let html = '';
    if (activeTriageTab === 'recommendations') {
        const items = (t.recommendations || []).slice(0, 8);
        html = items.length ? items.map(r => {
            const score = (r.score != null && isFinite(r.score)) ? r.score.toFixed(2) : '-';
            const reason = (r.reasons && r.reasons.length > 0) ? r.reasons[0] : '';
            return '<div class="triage-item" data-id="' + escapeHTML(r.id || '') + '">' +
                '<div class="triage-item-header"><span class="triage-item-id">' + escapeHTML(r.id || '-') + '</span><span class="triage-item-score">' + escapeHTML(score) + '</span></div>' +
                '<div class="triage-item-title">' + escapeHTML(r.title || '') + '</div>' +
                '<div class="triage-item-reason">' + escapeHTML(reason) + '</div></div>';
        }).join('') : '<div class="triage-item-reason">No recommendations</div>';
    } else if (activeTriageTab === 'quick_wins') {
        const items = (t.quick_wins || []).slice(0, 8);
        html = items.length ? items.map(w => {
            const score = (w.score != null && isFinite(w.score)) ? w.score.toFixed(2) : '-';
            return '<div class="triage-item" data-id="' + escapeHTML(w.id || '') + '">' +
                '<div class="triage-item-header"><span class="triage-item-id">⚡ ' + escapeHTML(w.id || '-') + '</span><span class="triage-item-score">' + escapeHTML(score) + '</span></div>' +
                '<div class="triage-item-title">' + escapeHTML(w.title || '') + '</div>' +
                '<div class="triage-item-reason">' + escapeHTML(w.reason || '') + '</div></div>';
        }).join('') : '<div class="triage-item-reason">No quick wins</div>';
    } else if (activeTriageTab === 'blockers') {
        const items = (t.blockers_to_clear || []).slice(0, 8);
        html = items.length ? items.map(b => {
            return '<div class="triage-item" data-id="' + escapeHTML(b.id || '') + '">' +
                '<div class="triage-item-header"><span class="triage-item-id">🔓 ' + escapeHTML(b.id || '-') + '</span><span class="triage-item-score">+' + escapeHTML(b.unblocks_count ?? 0) + '</span></div>' +
                '<div class="triage-item-title">' + escapeHTML(b.title || '') + '</div>' +
                '<div class="triage-item-reason">' + (b.actionable ? 'Actionable now' : 'Blocked upstream') + '</div></div>';
        }).join('') : '<div class="triage-item-reason">No blockers to clear</div>';
    } else if (activeTriageTab === 'alerts') {
        const items = (t.alerts || []).slice(0, 15);
        html = items.length ? items.map(a => {
            const id = a.issue_id || (Array.isArray(a.issue_ids) && a.issue_ids[0]) || '';
            return '<div class="triage-item" data-id="' + escapeHTML(id) + '">' +
                '<div class="triage-item-header"><span class="triage-item-id">' + escapeHTML((a.type || 'alert').toUpperCase()) + '</span><span class="triage-item-score">' + escapeHTML(a.severity || 'warning') + '</span></div>' +
                '<div class="triage-item-title">' + escapeHTML(a.message || '') + '</div>' +
                (id ? '<div class="triage-item-reason">Bead: ' + escapeHTML(id) + '</div>' : '') + '</div>';
        }).join('') : '<div class="triage-item-reason">No alerts</div>';
    } else if (activeTriageTab === 'high_risk') {
        const items = DATA.nodes
            .filter(n => isActiveNodeStatus(n.status) && (Number(n.risk_score) || 0) >= 0.5)
            .sort((a, b) => ((Number(b.risk_score) || 0) - (Number(a.risk_score) || 0)) || String(a.id).localeCompare(String(b.id)))
            .slice(0, 12);
        html = items.length ? items.map(n => {
            const pct = Math.round((Number(n.risk_score) || 0) * 100) + '%%';
            return '<div class="triage-item" data-id="' + escapeHTML(n.id || '') + '">' +
                '<div class="triage-item-header"><span class="triage-item-id">⚠ ' + escapeHTML(n.id || '-') + '</span><span class="triage-item-score" style="color:#f87171">' + escapeHTML(pct) + '</span></div>' +
                '<div class="triage-item-title">' + escapeHTML(n.title || '') + '</div>' +
                '<div class="triage-item-reason">' + escapeHTML(n.risk_explanation || 'Elevated operational risk') + '</div></div>';
        }).join('') : '<div class="triage-item-reason">No active high-risk issues (≥50%%)</div>';
    } else if (activeTriageTab === 'structural') {
        const adv = DATA.advanced_insights || {};
        const topK = Array.isArray(adv.top_k_set?.items) ? adv.top_k_set.items.slice(0, 5) : [];
        const pCuts = Array.isArray(adv.parallel_cut?.suggestions) ? adv.parallel_cut.suggestions.slice(0, 5) : [];
        const kPaths = Array.isArray(adv.k_paths?.paths) ? adv.k_paths.paths.slice(0, 5) : [];
        const parts = [];
        if (topK.length > 0) {
            parts.push('<div class="triage-item-reason" style="font-weight:700;color:#c7d2fe;margin:0.2rem 0 0.35rem">🔓 Top-K Unlock Sequence (+' + escapeHTML(adv.top_k_set?.total_gain ?? 0) + ')</div>');
            topK.forEach((it, idx) => {
                const unb = Array.isArray(it.unblocks) ? it.unblocks : [];
                const hlIds = [it.id, ...unb].filter(Boolean);
                parts.push('<div class="triage-item structural-item" data-id="' + escapeHTML(it.id || '') + '" data-path="' + escapeHTML(JSON.stringify(hlIds)) + '">' +
                    '<div class="triage-item-header"><span class="triage-item-id">#' + (idx + 1) + ' ' + escapeHTML(it.id || '-') + '</span><span class="triage-item-score">+' + escapeHTML(it.marginal_gain ?? 0) + '</span></div>' +
                    '<div class="triage-item-title">' + escapeHTML(it.title || '') + '</div>' +
                    (unb.length ? '<div class="triage-item-reason">Unblocks: ' + escapeHTML(unb.slice(0, 4).join(', ')) + (unb.length > 4 ? (' +' + (unb.length - 4)) : '') + '</div>' : '') + '</div>');
            });
        }
        if (pCuts.length > 0) {
            parts.push('<div class="triage-item-reason" style="font-weight:700;color:#a7f3d0;margin:0.5rem 0 0.35rem">⚡ Parallelism Unlockers</div>');
            pCuts.forEach(pc => {
                parts.push('<div class="triage-item" data-id="' + escapeHTML(pc.id || '') + '">' +
                    '<div class="triage-item-header"><span class="triage-item-id">' + escapeHTML(pc.id || '-') + '</span><span class="triage-item-score">Δ‖ +' + escapeHTML(pc.parallel_gain ?? 0) + '</span></div>' +
                    '<div class="triage-item-title">' + escapeHTML(pc.title || '') + '</div></div>');
            });
        }
        if (kPaths.length > 0) {
            parts.push('<div class="triage-item-reason" style="font-weight:700;color:#fde68a;margin:0.5rem 0 0.35rem">🔗 Critical Chains (K-Paths)</div>');
            kPaths.forEach((kp, idx) => {
                const ids = Array.isArray(kp.issue_ids) ? kp.issue_ids : [];
                parts.push('<div class="triage-item structural-path-item" data-id="' + escapeHTML(ids[0] || '') + '" data-path="' + escapeHTML(JSON.stringify(ids)) + '">' +
                    '<div class="triage-item-header"><span class="triage-item-id">Chain #' + (idx + 1) + '</span><span class="triage-item-score">' + escapeHTML(kp.length ?? ids.length) + ' nodes</span></div>' +
                    '<div class="triage-item-reason" style="font-family:monospace">' + escapeHTML(ids.slice(0, 5).join(' → ')) + (ids.length > 5 ? (' → … (+' + (ids.length - 5) + ')') : '') + '</div></div>');
            });
        }
        html = parts.length ? parts.join('') : '<div class="triage-item-reason">No structural unlock insights available</div>';
    }
    list.innerHTML = html;
    list.querySelectorAll('.triage-item').forEach(item => {
        item.onclick = () => {
            const rawPath = item.dataset.path;
            if (rawPath) {
                let parsedIds = [];
                try {
                    const parsed = JSON.parse(rawPath);
                    if (Array.isArray(parsed)) parsedIds = parsed;
                } catch (_) {
                    parsedIds = rawPath.split(',');
                }
                const visibleNodes = (Graph.graphData().nodes || []).filter(currentVisibilityFilter);
                const visibleSet = new Set(visibleNodes.map(n => n.id));
                const pathIds = parsedIds.filter(id => id && visibleSet.has(id));
                if (pathIds.length > 0) {
                    cancelPathFinder();
                    clearSelection();
                    hideHoverPanel();
                    highlightPath(pathIds);
                    const focusId = (item.dataset.id && visibleSet.has(item.dataset.id)) ? item.dataset.id : pathIds[0];
                    const targetNode = visibleNodes.find(n => n.id === focusId);
                    if (targetNode && Number.isFinite(targetNode.x) && Number.isFinite(targetNode.y)) {
                        Graph.centerAt(targetNode.x, targetNode.y, 400);
                    }
                    return;
                }
            }
            focusVisibleGraphNode(item.dataset.id);
        };
    });
}
document.querySelectorAll('.triage-tab-btn').forEach(btn => {
    btn.onclick = () => renderTriageTab(btn.dataset.tab);
});
document.getElementById('btn-triage').onclick = () => {
    const panel = document.getElementById('triage-panel');
    const btn = document.getElementById('btn-triage');
    const visible = panel.style.display === 'none';
    panel.style.display = visible ? 'block' : 'none';
    btn.classList.toggle('active', visible);
    if (visible) {
        const alertCount = (DATA.triage?.alerts || []).length;
        const alertTabBtn = document.querySelector('.triage-tab-btn[data-tab="alerts"]');
        if (alertTabBtn) alertTabBtn.textContent = alertCount > 0 ? ('Alerts (' + alertCount + ')') : 'Alerts';
        const highRiskCount = DATA.nodes.filter(n => isActiveNodeStatus(n.status) && (Number(n.risk_score) || 0) >= 0.5).length;
        const highRiskTabBtn = document.querySelector('.triage-tab-btn[data-tab="high_risk"]');
        if (highRiskTabBtn) highRiskTabBtn.textContent = highRiskCount > 0 ? ('High Risk (' + highRiskCount + ')') : 'High Risk';
        const crBox = document.getElementById('triage-cross-repo-summary');
        const crf = DATA.triage?.project_health?.cross_repo_flow;
        if (crBox && crf && Number(crf.total_cross_repo_deps) > 0) {
            const bRepos = Array.isArray(crf.bottleneck_repos) ? crf.bottleneck_repos.slice(0, 3) : [];
            const bnPart = bRepos.length ? (' · Bottlenecks: ' + bRepos.map(r => '<strong>' + escapeHTML(r) + '</strong>').join(', ')) : '';
            crBox.innerHTML = '🔀 <strong>' + escapeHTML(crf.total_cross_repo_deps) + '</strong> cross-repo edge' + (crf.total_cross_repo_deps === 1 ? '' : 's') + bnPart;
            crBox.style.display = 'block';
        } else if (crBox) {
            crBox.style.display = 'none';
            crBox.innerHTML = '';
        }
        renderTriageTab(activeTriageTab);
    }
};

// Top nodes panel
document.getElementById('btn-top').onclick = () => {
    const panel = document.getElementById('top-nodes-panel');
    const visible = panel.classList.toggle('visible');
    document.getElementById('btn-top').classList.toggle('active', visible);
    if (visible) {
        const rankedActive = DATA.nodes
            .filter(n => isActiveNodeStatus(n.status) && Number(n.pagerank_rank) > 0)
            .sort((a, b) => (Number(a.pagerank_rank) - Number(b.pagerank_rank)) || String(a.id).localeCompare(String(b.id)));
        const activeFallback = DATA.nodes
            .filter(n => isActiveNodeStatus(n.status))
            .sort((a, b) => ((b.pagerank || 0) - (a.pagerank || 0)) || String(a.id).localeCompare(String(b.id)));
        const sorted = (rankedActive.length > 0
            ? rankedActive
            : (activeFallback.length > 0
                ? activeFallback
                : [...DATA.nodes].sort((a, b) => ((b.pagerank || 0) - (a.pagerank || 0)) || String(a.id).localeCompare(String(b.id))))
        ).slice(0, 10);
        panel.innerHTML = sorted.map((n, i) => {
            const rankNum = Number(n.pagerank_rank) > 0 ? Number(n.pagerank_rank) : (i + 1);
            return '<div class="top-node-item" data-id="' + escapeHTML(n.id) + '"><span class="rank">#' + escapeHTML(rankNum) + '</span><span>' + escapeHTML(n.id) + '</span></div>';
        }).join('');
        panel.querySelectorAll('.top-node-item').forEach(el => {
            el.onclick = () => focusVisibleGraphNode(el.dataset.id);
        });
    }
};

// Fullscreen
document.getElementById('btn-fullscreen').onclick = () => {
    if (!document.fullscreenElement) container.requestFullscreen();
    else document.exitFullscreen();
};

// Toast
function showToast(msg) {
    const toast = document.getElementById('toast');
    toast.textContent = msg; toast.classList.add('visible');
    setTimeout(() => toast.classList.remove('visible'), 2500);
}

// Light/Dark mode toggle
function toggleLightMode() {
    isDarkMode = !isDarkMode;
    document.body.classList.toggle('light-mode', !isDarkMode);
    const btn = document.getElementById('btn-theme');
    btn.textContent = isDarkMode ? '☀️' : '🌙';
    btn.title = isDarkMode ? 'Switch to light mode (L)' : 'Switch to dark mode (L)';
    localStorage.setItem('bv-graph-theme', isDarkMode ? 'dark' : 'light');
    Graph.nodeColor(Graph.nodeColor());
    Graph.linkColor(computeLinkColor);
    Graph.linkDirectionalArrowColor(computeLinkArrowColor);
    updateMinimap();
}

// Recently viewed nodes
function addToRecent(node) {
    if (!node || !node.id) return;
    const idx = recentlyViewed.findIndex(n => n.id === node.id);
    if (idx > -1) recentlyViewed.splice(idx, 1);
    recentlyViewed.unshift({ id: node.id, title: node.title });
    if (recentlyViewed.length > MAX_RECENT) recentlyViewed.pop();
    updateRecentPanel();
}
function updateRecentPanel() {
    const list = document.getElementById('recent-list');
    list.innerHTML = recentlyViewed.map(n =>
        '<div class="recent-item" data-id="' + escapeHTML(n.id) + '"><span class="recent-id">' + escapeHTML(n.id) + '</span></div>'
    ).join('');
    list.querySelectorAll('.recent-item').forEach(el => {
        el.onclick = () => focusVisibleGraphNode(el.dataset.id);
    });
}
document.getElementById('btn-recent').onclick = () => {
    const panel = document.getElementById('recent-panel');
    const visible = panel.classList.toggle('visible');
    document.getElementById('btn-recent').classList.toggle('active', visible);
};

// Path finder mode
let pathFinderMode = false;
let pathStart = null;
function updatePathfinderBanner(text) {
    const banner = document.getElementById('pathfinder-banner');
    if (!banner) return;
    if (text) banner.textContent = text;
    banner.classList.toggle('visible', pathFinderMode);
}
function togglePathFinder() {
    closeCycleInspector();
    cancelCheckDep();
    clearWhatIfSimulation();
    pathFinderMode = !pathFinderMode;
    document.getElementById('btn-path').classList.toggle('active', pathFinderMode);
    if (!pathFinderMode) {
        pathStart = null;
        activePathNodes = null;
        highlightedNodes = new Set();
        updatePathfinderBanner('🔗 Path Finder: Click start node (Esc to cancel)');
        Graph.nodeColor(computeNodeColor).linkColor(Graph.linkColor()).linkWidth(Graph.linkWidth());
    } else if (selectedNode) {
        pathStart = selectedNode.id;
        activePathNodes = new Set([selectedNode.id]);
        highlightedNodes = new Set([selectedNode.id]);
        Graph.nodeColor(computeNodeColor).linkColor(Graph.linkColor()).linkWidth(Graph.linkWidth());
        updatePathfinderBanner('🔗 Path Finder: Start ' + selectedNode.id + ' — click destination node (Esc to cancel)');
        showToast('Start node: ' + selectedNode.id + ' — click destination node');
    } else {
        pathStart = null;
        activePathNodes = null;
        highlightedNodes = new Set();
        Graph.nodeColor(computeNodeColor).linkColor(Graph.linkColor()).linkWidth(Graph.linkWidth());
        updatePathfinderBanner('🔗 Path Finder: Click start node (Esc to cancel)');
        showToast('Path Finder: click start node');
    }
}
document.getElementById('btn-path').onclick = togglePathFinder;

// BFS for shortest path
function bfsPath(startId, endId) {
    const links = DATA.links;
    const adj = {};
    links.forEach(l => {
        const s = typeof l.source === 'object' ? l.source.id : l.source;
        const t = typeof l.target === 'object' ? l.target.id : l.target;
        if (!adj[s]) adj[s] = [];
        if (!adj[t]) adj[t] = [];
        adj[s].push(t);
        adj[t].push(s); // Undirected for path finding
    });
    const queue = [[startId]];
    const visited = new Set([startId]);
    while (queue.length > 0) {
        const path = queue.shift();
        const node = path[path.length - 1];
        if (node === endId) return path;
        for (const neighbor of (adj[node] || [])) {
            if (!visited.has(neighbor)) {
                visited.add(neighbor);
                queue.push([...path, neighbor]);
            }
        }
    }
    return null;
}

// Help overlay
function toggleHelp() {
    document.getElementById('help-overlay').classList.toggle('visible');
}
document.getElementById('btn-help').onclick = toggleHelp;
document.getElementById('help-close').onclick = toggleHelp;
document.getElementById('help-overlay').onclick = e => { if (e.target.id === 'help-overlay') toggleHelp(); };

// Mini-map
const minimapCanvas = document.getElementById('minimap-canvas');
const minimapCtx = minimapCanvas.getContext('2d');
function updateMinimap() {
    const nodes = Graph.graphData().nodes;
    if (nodes.length === 0) return;
    const bounds = { minX: Infinity, maxX: -Infinity, minY: Infinity, maxY: -Infinity };
    nodes.forEach(n => {
        if (n.x != null && n.y != null) {
            bounds.minX = Math.min(bounds.minX, n.x);
            bounds.maxX = Math.max(bounds.maxX, n.x);
            bounds.minY = Math.min(bounds.minY, n.y);
            bounds.maxY = Math.max(bounds.maxY, n.y);
        }
    });
    const padding = 20;
    const w = minimapCanvas.width = 160;
    const h = minimapCanvas.height = 120;
    const scaleX = (w - padding * 2) / (bounds.maxX - bounds.minX || 1);
    const scaleY = (h - padding * 2) / (bounds.maxY - bounds.minY || 1);
    const scale = Math.min(scaleX, scaleY);
    minimapCtx.fillStyle = isDarkMode ? '#1a1a2e' : '#f0f2f5';
    minimapCtx.fillRect(0, 0, w, h);
    nodes.forEach(n => {
        if (n.x == null || n.y == null) return;
        const x = padding + (n.x - bounds.minX) * scale;
        const y = padding + (n.y - bounds.minY) * scale;
        minimapCtx.fillStyle = computeNodeColor(n);
        minimapCtx.beginPath();
        minimapCtx.arc(x, y, 2, 0, Math.PI * 2);
        minimapCtx.fill();
    });
}
setInterval(updateMinimap, 2000);

// LocalStorage preferences
function loadPreferences() {
    const theme = localStorage.getItem('bv-graph-theme');
    if (theme === 'light') { isDarkMode = true; toggleLightMode(); }
    const layout = localStorage.getItem('bv-graph-layout');
    if (layout) {
        applyGraphDagMode(layout);
    }
}

// Keyboard shortcuts
document.onkeydown = e => {
    if (e.target && (['INPUT', 'SELECT', 'TEXTAREA'].includes(e.target.tagName) || e.target.isContentEditable)) return;
    if (e.key === '?') { toggleHelp(); return; }
    switch(e.key.toLowerCase()) {
        case 'f': Graph.zoomToFit(400, 50); break;
        case 'r': document.getElementById('btn-reset').click(); break;
        case 'escape':
            hideContextMenu();
            if (checkDepMode || checkDepFromId) { cancelCheckDep(); activePathNodes = null; highlightedNodes = new Set(); Graph.nodeColor(computeNodeColor).linkColor(Graph.linkColor()).linkWidth(Graph.linkWidth()); }
            else if (pathFinderMode) { pathFinderMode = false; pathStart = null; activePathNodes = null; highlightedNodes = new Set(); document.getElementById('pathfinder-banner').classList.remove('visible'); document.getElementById('btn-path').classList.remove('active'); Graph.nodeColor(computeNodeColor).linkColor(Graph.linkColor()).linkWidth(Graph.linkWidth()); }
            else { closeCycleInspector(); clearWhatIfSimulation(); clearSelection(); hideHoverPanel(); activePathNodes = null; highlightedNodes = new Set(); Graph.nodeColor(computeNodeColor).linkColor(Graph.linkColor()).linkWidth(Graph.linkWidth()); }
            break;
        case ' ': e.preventDefault(); document.getElementById('btn-fullscreen').click(); break;
        case 'h': document.getElementById('btn-heatmap').click(); break;
        case 't': document.getElementById('btn-top').click(); break;
        case 'g': document.getElementById('btn-triage').click(); break;
        case 'c': if (Array.isArray(DATA.cycles) && DATA.cycles.length > 0) document.getElementById('btn-cycles').click(); break;
        case 'd': togglePanelMode(); break;
        case 'l': toggleLightMode(); break;
        case 'y': document.getElementById('btn-recent').click(); break;
        case 'p': togglePathFinder(); break;
        case '1': applyGraphDagMode('force'); break;
        case '2': applyGraphDagMode('td'); break;
        case '3': applyGraphDagMode('lr'); break;
        case '4': applyGraphDagMode('radialout'); break;
    }
};

// Wire up theme button
document.getElementById('btn-theme').onclick = toggleLightMode;

// Load preferences and initial fit
loadPreferences();
setTimeout(() => { Graph.zoomToFit(400, 50); updateVisibleCount(); updateMinimap(); }, 800);
    </script>
</body>
</html>`, safeTitle, safeTitle, nodeCount, edgeCount, nodeCount, nodeCount, edgeCount, timestamp, safeHash, safeProject, forceGraphLib, markedLib, graphDataJSON)
	customStatusOpts, customTypeOpts := buildCustomFilterOptionsHTML(graphDataJSON)
	if customStatusOpts != "" {
		rendered = strings.Replace(rendered, `<option value="closed">Closed</option>`, `<option value="closed">Closed</option>`+customStatusOpts, 1)
	}
	if customTypeOpts != "" {
		rendered = strings.Replace(rendered, `<option value="epic">Epic</option>`, `<option value="epic">Epic</option>`+customTypeOpts, 1)
	}
	return rendered
}
