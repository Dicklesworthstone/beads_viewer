/**
 * bv Force-Graph Visualization Component
 *
 * Production-quality, high-performance graph visualization for dependency analysis.
 * Features: WASM-powered metrics, multiple view modes, rich interactions, accessibility.
 *
 * @module bv-graph
 * @version 1.0.0
 */

// ============================================================================
// THEME & CONSTANTS
// ============================================================================

const THEME_PALETTES = {
    dark: {
        bg: '#282a36',
        bgSecondary: '#44475a',
        bgTertiary: '#21222c',
        fg: '#f8f8f2',
        fgMuted: '#6272a4',
        linkDefault: '#44475a'
    },
    light: {
        bg: '#f8fafc',
        bgSecondary: '#ffffff',
        bgTertiary: '#f1f5f9',
        fg: '#0f172a',
        fgMuted: '#475569',
        linkDefault: '#94a3b8'
    }
};

let currentGraphDarkMode = true;

const THEME = {
    // Dracula-inspired palette (default dark; dynamically switched via setGraphTheme)
    bg: '#282a36',
    bgSecondary: '#44475a',
    bgTertiary: '#21222c',
    fg: '#f8f8f2',
    fgMuted: '#6272a4',

    // Status colors
    status: {
        open: '#50FA7B',
        in_progress: '#FFB86C',
        blocked: '#FF5555',
        closed: '#6272A4'
    },

    // Priority heat (flame intensity)
    priority: {
        0: '#FF0000',  // Critical
        1: '#FF5555',  // High
        2: '#FFB86C',  // Medium
        3: '#F1FA8C',  // Low
        4: '#6272A4'   // Backlog
    },

    // Accent colors
    accent: {
        purple: '#BD93F9',
        pink: '#FF79C6',
        cyan: '#8BE9FD',
        green: '#50FA7B',
        orange: '#FFB86C',
        red: '#FF5555',
        yellow: '#F1FA8C',
        gold: '#fbbf24'
    },

    // Link colors
    link: {
        default: '#44475a',
        highlighted: '#BD93F9',
        gold: '#fbbf24',
        goldGlow: 'rgba(251, 191, 36, 0.6)',
        critical: '#FF5555',
        cycle: '#FF79C6'
    }
};

const TYPE_ICONS = {
    bug: '\uD83D\uDC1B',      // 🐛
    feature: '\u2728',        // ✨
    task: '\uD83D\uDCDD',     // 📝
    epic: '\uD83C\uDFAF',     // 🎯
    chore: '\uD83D\uDD27',    // 🔧
    default: '\uD83D\uDCCB'   // 📋
};

const VIEW_MODES = {
    FORCE: 'force',           // Standard force-directed
    HIERARCHY: 'hierarchy',   // Top-down tree layout
    RADIAL: 'radial',         // Radial tree from selected node
    CLUSTER: 'cluster',       // Clustered by status
    LABEL_GALAXY: 'label_galaxy' // Clustered by label ("galaxy" view)
};

// Label color palette (10 distinct colors, colorblind-friendly)
const LABEL_COLORS = [
    '#8BE9FD', // Cyan
    '#50FA7B', // Green
    '#FFB86C', // Orange
    '#FF79C6', // Pink
    '#BD93F9', // Purple
    '#F1FA8C', // Yellow
    '#FF5555', // Red
    '#6272A4', // Comment (muted)
    '#44475A', // Selection
    '#F8F8F2'  // Foreground
];

// ============================================================================
// LAYOUT PRESETS (bv-97)
// ============================================================================

/**
 * Layout presets for different visualization needs.
 * Each preset configures force simulation parameters optimized for specific use cases.
 */
const LAYOUT_PRESETS = {
    // Default force-directed layout - balanced readability
    force: {
        name: 'Force-Directed',
        description: 'Standard physics-based layout with balanced spacing',
        icon: '🔄',
        config: {
            linkDistance: 100,
            chargeStrength: -150,
            centerStrength: 0.05,
            warmupTicks: 100,
            cooldownTicks: 300
        },
        viewMode: VIEW_MODES.FORCE
    },

    // Compact layout for large graphs - tighter spacing, faster settling
    compact: {
        name: 'Compact DAG',
        description: 'Tight layout for large graphs, emphasizes structure',
        icon: '📦',
        config: {
            linkDistance: 50,
            chargeStrength: -80,
            centerStrength: 0.15,
            warmupTicks: 50,
            cooldownTicks: 150
        },
        viewMode: VIEW_MODES.HIERARCHY,
        customForces: {
            // Stronger vertical alignment for DAG appearance
            yStrength: 0.4,
            depthSpacing: 80
        }
    },

    // Spread layout for clarity - more spacing, slower but cleaner
    spread: {
        name: 'Spread',
        description: 'Maximum spacing for readability, ideal for exports',
        icon: '🌟',
        config: {
            linkDistance: 180,
            chargeStrength: -300,
            centerStrength: 0.02,
            warmupTicks: 150,
            cooldownTicks: 500
        },
        viewMode: VIEW_MODES.FORCE
    },

    // Orthogonal-ish layout - grid-aligned, clean edges
    orthogonal: {
        name: 'Orthogonal',
        description: 'Grid-aligned layout with cleaner edge routing',
        icon: '📐',
        config: {
            linkDistance: 120,
            chargeStrength: -100,
            centerStrength: 0.1,
            warmupTicks: 200,
            cooldownTicks: 400
        },
        viewMode: VIEW_MODES.FORCE,
        customForces: {
            // Snap to grid forces
            gridSize: 60,
            gridStrength: 0.3
        }
    },

    // Radial layout - emanates from center/selection
    radial: {
        name: 'Radial',
        description: 'Circular layout from center or selected node',
        icon: '🎯',
        config: {
            linkDistance: 80,
            chargeStrength: -100,
            centerStrength: 0.01,
            warmupTicks: 100,
            cooldownTicks: 300
        },
        viewMode: VIEW_MODES.RADIAL
    },

    // Cluster layout - groups by status
    cluster: {
        name: 'Status Clusters',
        description: 'Groups nodes by status (open, in_progress, blocked)',
        icon: '🔲',
        config: {
            linkDistance: 70,
            chargeStrength: -60,
            centerStrength: 0.05,
            warmupTicks: 100,
            cooldownTicks: 250
        },
        viewMode: VIEW_MODES.CLUSTER
    },

    // Label galaxy layout - groups by primary label with convex hulls (bv-qpt0)
    galaxy: {
        name: 'Label Galaxy',
        description: 'Clusters nodes by primary label with color-coded hulls',
        icon: '🌌',
        config: {
            linkDistance: 80,
            chargeStrength: -40,
            centerStrength: 0.05,
            warmupTicks: 100,
            cooldownTicks: 300
        },
        viewMode: VIEW_MODES.LABEL_GALAXY
    }
};

// Default preset for exports
const DEFAULT_EXPORT_PRESET = 'spread';

// ============================================================================
// STATE MANAGEMENT
// ============================================================================

class GraphStore {
    constructor() {
        this.graph = null;
        this.wasmGraph = null;
        this.activeWasmGraph = null;
        this.whatIfWasmGraph = null;
        this.whatIfModel = null;
        this.wasmReady = false;
        this.container = null;
        this.resizeObserver = null;

        // Data
        this.issues = [];
        this.dependencies = [];
        this.nodeMap = new Map();      // id -> issue
        this.nodeIndexMap = new Map(); // id -> index
        this.nodeIdByIndex = [];       // index -> id

        // Computed metrics
        this.metrics = {
            pagerank: null,
            betweenness: null,
            criticalPath: null,
            eigenvector: null,
            kcore: null,
            hits: null,           // { hub: Float32Array, authority: Float32Array }
            slack: null,          // Float32Array of slack values
            articulationPoints: null, // Set of node IDs (cut vertices)
            bridges: null,        // Set of "a-b" / "b-a" Tarjan bridge cut-edge keys on active subgraph
            cycleBreakSuggestions: null, // Resolved [{ from, to, edgeKey, cyclesBroken }]
            cycles: null
        };

        // UI State
        this.viewMode = VIEW_MODES.FORCE;
        this.currentPreset = 'force'; // Layout preset (bv-97)
        this.selectedNode = null;
        this.hoveredNode = null;
        this.highlightedNodes = new Set();
        this.highlightedLinks = new Set();
        this.connectedNodes = new Set(); // Gold glow connected subgraph
        this.focusedPath = null;
        this.checkDepPicking = false;

        // Heatmap & metrics mode
        this.heatmapMode = false;
        this.sizeMetric = 'pagerank'; // pagerank | betweenness | eigenvector | inDegree | outDegree | criticalPath | kcore | hitsHub | hitsAuth | slack
        this.maxMetrics = {
            pagerank: 1,
            betweenness: 1,
            eigenvector: 1,
            inDegree: 1,
            indegree: 1,
            outDegree: 1,
            criticalPath: 1,
            critical: 1,
            kcore: 1,
            hitsHub: 1,
            hitsAuth: 1,
            slack: 1
        };

        // Filters
        this.filters = {
            status: null,
            priority: null,
            labels: [],
            repo: null,
            search: '',
            showClosed: true  // Default to showing all issues in static export
        };

        // Config
        this.config = {
            nodeMinSize: 4,
            nodeMaxSize: 24,
            linkDistance: 100,
            chargeStrength: -150,
            centerStrength: 0.05,
            warmupTicks: 100,
            cooldownTicks: 300,
            enableParticles: true,
            particleSpeed: 0.005,
            showLabels: true,
            labelZoomThreshold: 0.6
        };

        // Animation state
        this.animationFrame = null;
        this.particlePositions = new Map();
    }

    reset() {
        this.issues = [];
        this.dependencies = [];
        this.nodeMap.clear();
        this.nodeIndexMap.clear();
        this.nodeIdByIndex = [];
        // A skipped or failed computation belongs to this load, not the
        // previous graph. In particular, empty graphs skip betweenness.
        for (const metric of Object.keys(this.metrics)) this.metrics[metric] = null;
        this.selectedNode = null;
        this.hoveredNode = null;
        this.highlightedNodes.clear();
        this.highlightedLinks.clear();
        this.connectedNodes.clear();
        this.focusedPath = null;
        this.checkDepPicking = false;
        this.heatmapMode = false;
        this.filters = {
            status: null,
            priority: null,
            labels: [],
            repo: null,
            search: '',
            showClosed: true
        };
    }
}

const store = new GraphStore();

/**
 * Helper to force ForceGraph to redraw without disturbing the simulation.
 * Uses a micro zoom adjustment trick - imperceptible to users but forces canvas redraw.
 * This avoids the "wiggle" caused by graphData() potentially reheating the simulation.
 */
function refreshGraph() {
    if (!store.graph) return;
    // Get current zoom level
    const currentZoom = store.graph.zoom();
    // Guard against undefined/NaN zoom values
    if (typeof currentZoom !== 'number' || isNaN(currentZoom)) return;
    // Apply imperceptible zoom change (0.0001% = invisible) with 0ms duration (instant)
    store.graph.zoom(currentZoom * 1.000001, 0);
    // Immediately restore - this triggers a redraw without any visual change
    requestAnimationFrame(() => {
        if (store.graph) store.graph.zoom(currentZoom, 0);
    });
}

// ============================================================================
// HEATMAP & GOLD GLOW HELPERS
// ============================================================================

const SUPPORTED_SIZE_METRICS = [
    'pagerank',
    'betweenness',
    'eigenvector',
    'inDegree',
    'indegree',
    'outDegree',
    'criticalPath',
    'critical',
    'kcore',
    'hitsHub',
    'hitsAuth',
    'slack'
];

function normalizeMetricKey(metric) {
    if (metric === 'critical') return 'criticalPath';
    if (metric === 'indegree') return 'inDegree';
    return metric || 'pagerank';
}

function finiteMetricValue(val) {
    return typeof val === 'number' && Number.isFinite(val) ? val : 0;
}

/**
 * Compute max metric values for normalization
 * Note: Must be called AFTER prepareGraphData since metrics are on prepared nodes
 */
function computeMaxMetrics(nodesArg = null) {
    // Get prepared nodes from graph data if available, otherwise use raw issues
    const graphData = store.graph?.graphData();
    const nodes = nodesArg || graphData?.nodes || store.issues;
    if (!nodes.length) return;

    store.maxMetrics.pagerank = Math.max(...nodes.map(n => finiteMetricValue(n.pagerank)), 0.001);
    store.maxMetrics.betweenness = Math.max(...nodes.map(n => finiteMetricValue(n.betweenness)), 0.001);
    store.maxMetrics.eigenvector = Math.max(...nodes.map(n => finiteMetricValue(n.eigenvector)), 0.001);
    store.maxMetrics.inDegree = Math.max(...nodes.map(n => finiteMetricValue(n.inDegree ?? n.dependentCount)), 1);
    store.maxMetrics.indegree = store.maxMetrics.inDegree;
    store.maxMetrics.outDegree = Math.max(...nodes.map(n => finiteMetricValue(n.outDegree ?? n.blockerCount)), 1);
    store.maxMetrics.criticalPath = Math.max(...nodes.map(n => finiteMetricValue(n.criticalDepth)), 1);
    store.maxMetrics.critical = store.maxMetrics.criticalPath;
    store.maxMetrics.kcore = Math.max(...nodes.map(n => finiteMetricValue(n.kcore)), 1);
    store.maxMetrics.hitsHub = Math.max(...nodes.map(n => finiteMetricValue(n.hitsHub)), 0.001);
    store.maxMetrics.hitsAuth = Math.max(...nodes.map(n => finiteMetricValue(n.hitsAuth)), 0.001);
    store.maxMetrics.slack = Math.max(...nodes.map(n => finiteMetricValue(n.slack)), 1);
}

function getMetricRatio(node, metric = store.sizeMetric, forHeatmap = false) {
    const key = normalizeMetricKey(metric);
    let val = 0;
    let max = 1;
    switch (key) {
        case 'pagerank':
            val = finiteMetricValue(node.pagerank);
            max = store.maxMetrics.pagerank || 0.001;
            break;
        case 'betweenness':
            val = finiteMetricValue(node.betweenness);
            max = store.maxMetrics.betweenness || 0.001;
            break;
        case 'eigenvector':
            val = finiteMetricValue(node.eigenvector);
            max = store.maxMetrics.eigenvector || 0.001;
            break;
        case 'inDegree':
            val = finiteMetricValue(node.inDegree ?? node.dependentCount);
            max = store.maxMetrics.inDegree || 1;
            break;
        case 'outDegree':
            val = finiteMetricValue(node.outDegree ?? node.blockerCount);
            max = store.maxMetrics.outDegree || 1;
            break;
        case 'criticalPath':
            val = finiteMetricValue(node.criticalDepth);
            max = store.maxMetrics.criticalPath || 1;
            break;
        case 'kcore':
            val = finiteMetricValue(node.kcore);
            max = store.maxMetrics.kcore || 1;
            break;
        case 'hitsHub':
            val = finiteMetricValue(node.hitsHub);
            max = store.maxMetrics.hitsHub || 0.001;
            break;
        case 'hitsAuth':
            val = finiteMetricValue(node.hitsAuth);
            max = store.maxMetrics.hitsAuth || 0.001;
            break;
        case 'slack': {
            if (node.slack === undefined || node.slack === null || !Number.isFinite(node.slack)) {
                return 0;
            }
            max = store.maxMetrics.slack || 1;
            const slackRatio = Math.max(0, Math.min(node.slack / max, 1));
            // In heatmap mode, 0 slack (critical path) is hottest (1.0 -> red)
            return forHeatmap ? (1 - slackRatio) : slackRatio;
        }
        default:
            val = finiteMetricValue(node.pagerank);
            max = store.maxMetrics.pagerank || 0.001;
            break;
    }
    if (max <= 0) return 0;
    return Math.max(0, Math.min(val / max, 1));
}

/**
 * Get heatmap color for a node based on current sizeMetric
 * Uses HSL: 120 (green) to 0 (red) based on metric intensity
 */
function getHeatmapColor(node) {
    const ratio = getMetricRatio(node, store.sizeMetric, true);
    const hue = 120 - ratio * 120; // Green (120) to Red (0)
    return `hsl(${hue}, 80%, 50%)`;
}

/**
 * Get connected subgraph nodes via BFS (for gold glow highlight)
 * @param {string} nodeId - Starting node ID
 * @param {number} depth - Max depth to traverse (default 2)
 * @returns {Set<string>} Set of connected node IDs
 */
function getConnectedNodes(nodeId, depth = 2) {
    const connected = new Set([nodeId]);
    const queue = [{ id: nodeId, d: 0 }];
    const graphData = store.graph?.graphData();
    if (!graphData) return connected;

    while (queue.length > 0) {
        const { id, d } = queue.shift();
        if (d >= depth) continue;

        graphData.links.forEach(l => {
            const src = typeof l.source === 'object' ? l.source.id : l.source;
            const tgt = typeof l.target === 'object' ? l.target.id : l.target;
            if (src === id && !connected.has(tgt)) {
                connected.add(tgt);
                queue.push({ id: tgt, d: d + 1 });
            }
            if (tgt === id && !connected.has(src)) {
                connected.add(src);
                queue.push({ id: src, d: d + 1 });
            }
        });
    }
    return connected;
}

/**
 * Update connected nodes for gold glow effect
 */
function updateConnectedNodes(node) {
    if (node?.id) {
        store.connectedNodes = getConnectedNodes(node.id, 2);
    } else {
        store.connectedNodes.clear();
    }
}

/**
 * Toggle heatmap mode
 */
export function toggleHeatmap() {
    store.heatmapMode = !store.heatmapMode;
    refreshGraph();
    dispatchEvent('heatmapToggle', { active: store.heatmapMode, metric: store.sizeMetric });
    return store.heatmapMode;
}

/**
 * Set the size/heatmap metric
 */
export function setSizeMetric(metric) {
    if (SUPPORTED_SIZE_METRICS.includes(metric)) {
        store.sizeMetric = metric;
        computeMaxMetrics();
        refreshGraph();
        dispatchEvent('metricChange', { metric: store.sizeMetric });
    }
}

/**
 * Get current heatmap state
 */
export function getHeatmapState() {
    return {
        active: store.heatmapMode,
        metric: store.sizeMetric
    };
}

// ============================================================================
// LABEL CLUSTERING STATE
// ============================================================================

const labelClusterState = {
    active: false,
    labels: [],                    // Unique labels in dataset
    labelColorMap: new Map(),      // label -> color
    labelCenters: new Map(),       // label -> {x, y} center position
    clusterHulls: new Map(),       // label -> hull points array
    showHulls: true,
    showLegend: true,
    crossLabelEdges: new Set()     // Set of edge IDs crossing label boundaries
};

/**
 * Build label color map and compute cluster centers
 */
function buildLabelColorMap() {
    labelClusterState.labelColorMap.clear();
    labelClusterState.labelCenters.clear();

    // Collect all unique labels
    const labelSet = new Set();
    store.issues.forEach(issue => {
        (issue.labels || []).forEach(label => labelSet.add(label));
    });

    // Add "unlabeled" for issues without labels
    labelSet.add('(unlabeled)');

    labelClusterState.labels = [...labelSet].sort();

    // Assign colors
    labelClusterState.labels.forEach((label, i) => {
        labelClusterState.labelColorMap.set(label, LABEL_COLORS[i % LABEL_COLORS.length]);
    });

    // Compute initial cluster centers in a circle
    const numLabels = labelClusterState.labels.length;
    const radius = Math.max(200, numLabels * 30);
    labelClusterState.labels.forEach((label, i) => {
        const angle = (2 * Math.PI * i) / numLabels - Math.PI / 2; // Start from top
        labelClusterState.labelCenters.set(label, {
            x: Math.cos(angle) * radius,
            y: Math.sin(angle) * radius
        });
    });

    return labelClusterState.labelColorMap;
}

/**
 * Get the primary label for a node (first label or 'unlabeled')
 */
function getPrimaryLabel(node) {
    return (node.labels && node.labels.length > 0) ? node.labels[0] : '(unlabeled)';
}

/**
 * Get the color for a node based on its primary label
 */
function getLabelColor(node) {
    const label = getPrimaryLabel(node);
    return labelClusterState.labelColorMap.get(label) || LABEL_COLORS[0];
}

/**
 * Check if an edge crosses label boundaries
 */
function isCrossLabelEdge(link) {
    const sourceNode = typeof link.source === 'object' ? link.source : store.nodeMap.get(link.source);
    const targetNode = typeof link.target === 'object' ? link.target : store.nodeMap.get(link.target);

    if (!sourceNode || !targetNode) return false;

    const sourceLabel = getPrimaryLabel(sourceNode);
    const targetLabel = getPrimaryLabel(targetNode);

    return sourceLabel !== targetLabel;
}

/**
 * Compute convex hulls for each label cluster
 */
function computeClusterHulls() {
    if (!store.graph) return;

    labelClusterState.clusterHulls.clear();
    const graphData = store.graph.graphData();

    // Group nodes by primary label
    const labelGroups = new Map();
    graphData.nodes.forEach(node => {
        const label = getPrimaryLabel(node);
        if (!labelGroups.has(label)) {
            labelGroups.set(label, []);
        }
        labelGroups.get(label).push([node.x, node.y]);
    });

    // Compute hull for each group with 3+ nodes
    labelGroups.forEach((points, label) => {
        if (points.length >= 3) {
            // Use d3.polygonHull for convex hull computation
            const hull = d3.polygonHull(points);
            if (hull) {
                labelClusterState.clusterHulls.set(label, hull);
            }
        } else if (points.length > 0) {
            // For 1-2 nodes, just store the points
            labelClusterState.clusterHulls.set(label, points);
        }
    });

    return labelClusterState.clusterHulls;
}

/**
 * Draw cluster hulls on the canvas (bv-qpt0)
 * Called during onRenderFramePre to draw behind nodes
 */
function drawClusterHulls(ctx, globalScale) {
    // Recompute hulls if needed (layout may have changed)
    computeClusterHulls();

    labelClusterState.clusterHulls.forEach((hull, label) => {
        if (!hull || hull.length < 3) return;

        const color = labelClusterState.labelColorMap.get(label) || LABEL_COLORS[0];

        ctx.save();

        // Draw semi-transparent filled hull
        ctx.beginPath();
        ctx.moveTo(hull[0][0], hull[0][1]);
        for (let i = 1; i < hull.length; i++) {
            ctx.lineTo(hull[i][0], hull[i][1]);
        }
        ctx.closePath();

        // Fill with label color at low opacity
        ctx.fillStyle = color + '15'; // ~8% opacity
        ctx.fill();

        // Draw hull border
        ctx.strokeStyle = color + '40'; // ~25% opacity
        ctx.lineWidth = Math.max(1, 2 / globalScale);
        ctx.setLineDash([5 / globalScale, 5 / globalScale]);
        ctx.stroke();
        ctx.setLineDash([]);

        // Draw label name at cluster center (when zoomed out enough)
        if (globalScale < 0.8 && hull.length >= 3) {
            // Compute centroid
            let cx = 0, cy = 0;
            hull.forEach(point => {
                cx += point[0];
                cy += point[1];
            });
            cx /= hull.length;
            cy /= hull.length;

            // Draw label text - keep ~14px on screen regardless of zoom
            const fontSize = Math.min(14, Math.max(5, 14 / globalScale));
            ctx.font = `600 ${fontSize}px 'Inter', sans-serif`;
            ctx.textAlign = 'center';
            ctx.textBaseline = 'middle';
            ctx.fillStyle = color + '99'; // ~60% opacity
            ctx.fillText(label, cx, cy);
        }

        ctx.restore();
    });
}

// ============================================================================
// WASM INTEGRATION
// ============================================================================

async function initWasm() {
    try {
        if (typeof window.bvGraphWasm !== 'undefined') {
            await window.bvGraphWasm.default();
            store.wasmReady = true;
            console.log('[bv-graph] WASM initialized, version:', window.bvGraphWasm.version());
            return true;
        }
    } catch (e) {
        console.warn('[bv-graph] WASM init failed:', e);
    }
    store.wasmReady = false;
    return false;
}

// Keep graph construction and rendered links aligned with the Go loader.
function isBlockingDependency(dependency) {
    return !dependency.type || ['blocks', 'conditional-blocks', 'waits-for'].includes(dependency.type);
}

// Allow all custom and built-in non-blocking Beads edges to render visually (dashed)
// while keeping WASM DAG metrics strictly blocking-only via isBlockingDependency.
function isRenderableDependency(dependency) {
    return Boolean(dependency && dependency.issue_id && dependency.depends_on_id);
}

function isResolvedIssue(issue) {
    const st = String(issue?.status || '').trim().toLowerCase();
    return st === 'closed' || st === 'tombstone';
}

function resolveGraphReferenceNowMs() {
    const viewerNow = window.beadsViewer?.getReferenceNowMs?.();
    if (Number.isFinite(viewerNow) && viewerNow > 0) {
        return viewerNow;
    }
    const globalNow = Number(window.BV_REFERENCE_NOW_MS);
    if (Number.isFinite(globalNow) && globalNow > 0) {
        return globalNow;
    }
    return Date.now();
}

function isTruthyGraphFlag(val) {
    return val === true || val === 'true' || Number(val) > 0;
}

function isGraphIssueLifecycleWithheld(issue, refNowMs) {
    if (!issue) return true;
    const st = String(issue.status || 'open').trim().toLowerCase();
    if (st !== 'open' && st !== 'in_progress') return true;
    if (
        isTruthyGraphFlag(issue.is_template) ||
        isTruthyGraphFlag(issue.pinned) ||
        isTruthyGraphFlag(issue.ephemeral)
    ) {
        return true;
    }
    if (issue.defer_until) {
        const deferMs = Date.parse(issue.defer_until);
        if (Number.isFinite(deferMs) && deferMs > refNowMs) return true;
    }
    return false;
}

function buildCompanionWhatIfWasmGraph(DiGraph, resolvedIds) {
    const baseGraph = store.wasmGraph;
    const realCount = baseGraph.nodeCount();
    const wg = DiGraph.withCapacity(realCount + 1, store.dependencies.length);
    for (let i = 0; i < realCount; i++) {
        wg.addNode(baseGraph.nodeId(i));
    }

    const directBlockers = Array.from({ length: realCount }, () => []);
    const directDependents = Array.from({ length: realCount }, () => []);
    const openParents = Array.from({ length: realCount }, () => []);
    const openChildren = Array.from({ length: realCount }, () => []);
    const hasDirectMissingParent = new Uint8Array(realCount);
    const baseClosed = new Uint8Array(realCount);
    const lifecycleReady = new Uint8Array(realCount);
    let hasParentChild = false;

    const directBlockingById = new Map();
    for (const d of store.dependencies) {
        if (!isBlockingDependency(d) || !d.issue_id || !d.depends_on_id) continue;
        const fromIdx = baseGraph.nodeIdx(d.issue_id);
        const toIdx = baseGraph.nodeIdx(d.depends_on_id);
        if (fromIdx === undefined || toIdx === undefined) continue;
        wg.addEdge(fromIdx, toIdx);
        directBlockers[fromIdx].push(toIdx);
        directDependents[toIdx].push(fromIdx);
        let list = directBlockingById.get(d.issue_id);
        if (!list) {
            list = [];
            directBlockingById.set(d.issue_id, list);
        }
        list.push(toIdx);
    }

    const knownIssues = new Map();
    for (const issue of store.issues) {
        if (issue && issue.id) knownIssues.set(issue.id, issue);
    }

    const openParentsById = new Map();
    const childrenById = new Map();
    const pendingParents = new Map();
    const hasMissingParent = new Set();

    for (const id of knownIssues.keys()) {
        if (!resolvedIds.has(id)) {
            openParentsById.set(id, []);
            pendingParents.set(id, 0);
        }
    }

    for (const d of store.dependencies) {
        if (!d || d.type !== 'parent-child') continue;
        const childId = d.issue_id;
        const parentId = d.depends_on_id;
        if (!childId || resolvedIds.has(childId) || !knownIssues.has(childId)) continue;
        if (!parentId || resolvedIds.has(parentId)) {
            // Closed or tombstone parent stops ancestor gate inheritance.
            continue;
        }
        const childIdx = baseGraph.nodeIdx(childId);
        if (!knownIssues.has(parentId)) {
            hasMissingParent.add(childId);
            if (childIdx !== undefined && childIdx < realCount) {
                hasDirectMissingParent[childIdx] = 1;
                hasParentChild = true;
            }
            continue;
        }
        hasParentChild = true;
        const parentIdx = baseGraph.nodeIdx(parentId);
        if (childIdx !== undefined && parentIdx !== undefined && childIdx < realCount && parentIdx < realCount) {
            openParents[childIdx].push(parentIdx);
            openChildren[parentIdx].push(childIdx);
        }
        openParentsById.get(childId).push(parentId);
        pendingParents.set(childId, (pendingParents.get(childId) || 0) + 1);
        let kids = childrenById.get(parentId);
        if (!kids) {
            kids = [];
            childrenById.set(parentId, kids);
        }
        kids.push(childId);
    }

    const inheritedPrereqs = new Map();
    const inheritedUnknown = new Set();
    const queue = [];
    for (const [id, deg] of pendingParents.entries()) {
        if (deg === 0) queue.push(id);
    }
    for (let head = 0; head < queue.length; head++) {
        const u = queue[head];
        const reqSet = new Set();
        let unknown = hasMissingParent.has(u);
        for (const p of openParentsById.get(u) || []) {
            if (inheritedUnknown.has(p)) unknown = true;
            for (const bIdx of directBlockingById.get(p) || []) {
                reqSet.add(bIdx);
            }
            const pInherited = inheritedPrereqs.get(p);
            if (pInherited) {
                for (const bIdx of pInherited) reqSet.add(bIdx);
            }
        }
        inheritedPrereqs.set(u, reqSet);
        if (unknown) inheritedUnknown.add(u);

        for (const childId of childrenById.get(u) || []) {
            const rem = (pendingParents.get(childId) || 0) - 1;
            pendingParents.set(childId, rem);
            if (rem === 0) queue.push(childId);
        }
    }

    const refNowMs = resolveGraphReferenceNowMs();
    let sentinelIdx = null;
    const ensureSentinel = () => {
        if (sentinelIdx === null) {
            sentinelIdx = wg.addNode('__bv_whatif_unresolved_sentinel__');
        }
        return sentinelIdx;
    };

    for (let idx = 0; idx < realCount; idx++) {
        const id = baseGraph.nodeId(idx);
        if (!id) continue;
        if (resolvedIds.has(id)) {
            baseClosed[idx] = 1;
            continue;
        }
        const issue = knownIssues.get(id);
        if (!issue) {
            wg.addEdge(idx, ensureSentinel());
            continue;
        }
        const withheld = isGraphIssueLifecycleWithheld(issue, refNowMs);
        if (!withheld) {
            lifecycleReady[idx] = 1;
        }
        const inherited = inheritedPrereqs.get(id);
        if (inherited) {
            for (const bIdx of inherited) {
                wg.addEdge(idx, bIdx);
            }
        }
        const inParentCycle = (pendingParents.get(id) || 0) > 0;
        if (hasMissingParent.has(id) || inheritedUnknown.has(id) || inParentCycle) {
            wg.addEdge(idx, ensureSentinel());
        } else if (withheld) {
            wg.addEdge(idx, ensureSentinel());
        }
    }

    store.whatIfModel = {
        realCount,
        hasParentChild,
        baseClosed,
        lifecycleReady,
        hasDirectMissingParent,
        directBlockers,
        directDependents,
        openParents,
        openChildren,
    };

    return wg;
}

function createGraphWhatIfDepsEvaluator(model, closedMask) {
    const memo = new Uint8Array(model.realCount);
    const visiting = new Uint8Array(model.realCount);

    function check(cur) {
        if (closedMask[cur]) return { ok: true, cyclic: false };
        const cached = memo[cur];
        if (cached === 1) return { ok: true, cyclic: false };
        if (cached === 2) return { ok: false, cyclic: false };
        if (visiting[cur]) return { ok: false, cyclic: true };

        visiting[cur] = 1;
        let ok = true;
        let cyclic = false;

        if (model.hasDirectMissingParent[cur]) {
            ok = false;
        } else {
            const blockers = model.directBlockers[cur];
            for (let i = 0; i < blockers.length; i++) {
                if (!closedMask[blockers[i]]) {
                    ok = false;
                    break;
                }
            }
            if (ok) {
                const parents = model.openParents[cur];
                for (let i = 0; i < parents.length; i++) {
                    const pIdx = parents[i];
                    if (closedMask[pIdx]) continue;
                    const res = check(pIdx);
                    if (res.cyclic) cyclic = true;
                    if (!res.ok) {
                        ok = false;
                        break;
                    }
                }
            }
        }

        visiting[cur] = 0;
        if (!cyclic) {
            memo[cur] = ok ? 1 : 2;
        }
        return { ok, cyclic };
    }

    return nodeIdx => check(nodeIdx).ok;
}

function evaluateGraphWhatIfReadyMask(model, closedMask) {
    const ready = new Uint8Array(model.realCount);
    const depsOk = createGraphWhatIfDepsEvaluator(model, closedMask);
    for (let i = 0; i < model.realCount; i++) {
        if (!closedMask[i] && model.lifecycleReady[i] && depsOk(i)) {
            ready[i] = 1;
        }
    }
    return ready;
}

function simulateGraphWhatIfCloseIndices(model, targetIndices) {
    const realCount = model.realCount;
    const uniqueTargets = [];
    const seenTargets = new Set();
    for (const idx of targetIndices) {
        if (idx === undefined || idx < 0 || idx >= realCount || model.baseClosed[idx] || seenTargets.has(idx)) {
            continue;
        }
        seenTargets.add(idx);
        uniqueTargets.push(idx);
    }
    if (uniqueTargets.length === 0) {
        return {
            direct_unblocks: 0,
            transitive_unblocks: 0,
            unblocked_ids: [],
            cascade_ids: [],
            parallel_gain: 0,
        };
    }

    const beforeReady = evaluateGraphWhatIfReadyMask(model, model.baseClosed);
    const simClosed = new Uint8Array(model.baseClosed);
    for (const idx of uniqueTargets) {
        simClosed[idx] = 1;
    }
    const afterReady = evaluateGraphWhatIfReadyMask(model, simClosed);

    const unblockedIds = [];
    for (let u = 0; u < realCount; u++) {
        if (!simClosed[u] && !beforeReady[u] && afterReady[u]) {
            unblockedIds.push(u);
        }
    }

    const visited = new Uint8Array(realCount);
    const cascadeIds = [];
    const queue = [];
    for (const root of unblockedIds) {
        visited[root] = 1;
        cascadeIds.push(root);
        queue.push(root);
    }

    for (let head = 0; head < queue.length; head++) {
        const v = queue[head];
        simClosed[v] = 1;

        const candSeen = new Uint8Array(realCount);
        const expandSubtreeQueue = [];
        const candidates = [];
        const enqueueCandidate = x => {
            if (candSeen[x]) return;
            candSeen[x] = 1;
            candidates.push(x);
            if (!simClosed[x]) {
                expandSubtreeQueue.push(x);
            }
        };

        for (const d of model.directDependents[v]) enqueueCandidate(d);
        for (const c of model.openChildren[v]) enqueueCandidate(c);

        for (let sHead = 0; sHead < expandSubtreeQueue.length; sHead++) {
            const p = expandSubtreeQueue[sHead];
            for (const c of model.openChildren[p]) {
                enqueueCandidate(c);
            }
        }

        if (candidates.length === 0) continue;
        candidates.sort((a, b) => a - b);
        const depsOk = createGraphWhatIfDepsEvaluator(model, simClosed);
        for (const w of candidates) {
            if (!visited[w] && !simClosed[w] && model.lifecycleReady[w] && depsOk(w)) {
                visited[w] = 1;
                cascadeIds.push(w);
                queue.push(w);
            }
        }
    }

    return {
        direct_unblocks: unblockedIds.length,
        transitive_unblocks: cascadeIds.length,
        unblocked_ids: unblockedIds,
        cascade_ids: cascadeIds,
        parallel_gain: Math.max(0, unblockedIds.length - 1),
    };
}

function buildWasmGraph() {
    if (!store.wasmReady) return;

    try {
        const { DiGraph } = window.bvGraphWasm;

        if (store.whatIfWasmGraph) {
            store.whatIfWasmGraph.free();
            store.whatIfWasmGraph = null;
        }
        store.whatIfModel = null;
        if (store.wasmGraph) {
            store.wasmGraph.free();
            store.wasmGraph = null;
        }
        if (store.activeWasmGraph) {
            store.activeWasmGraph.free();
            store.activeWasmGraph = null;
        }

        const resolvedIds = new Set(window.beadsViewer?.getResolvedIssueIDs?.() || []);
        store.issues.forEach(issue => {
            if (isResolvedIssue(issue)) {
                resolvedIds.add(issue.id);
            }
        });

        store.wasmGraph = DiGraph.withCapacity(store.issues.length, store.dependencies.length);
        store.activeWasmGraph = DiGraph.withCapacity(store.issues.length, store.dependencies.length);

        // Add all nodes to full graph; exclude closed/tombstone from active graph
        store.issues.forEach(issue => {
            store.wasmGraph.addNode(issue.id);
            if (!resolvedIds.has(issue.id)) {
                store.activeWasmGraph.addNode(issue.id);
            }
        });

        // Add blocking edges
        store.dependencies
            .filter(isBlockingDependency)
            .forEach(d => {
                // Keep omitted prerequisites in the analysis even though they
                // have no rendered node. Unknown references remain blockers.
                const fromIdx = store.wasmGraph.addNode(d.issue_id);
                const toIdx = store.wasmGraph.addNode(d.depends_on_id);
                store.wasmGraph.addEdge(fromIdx, toIdx);

                if (!resolvedIds.has(d.issue_id) && !resolvedIds.has(d.depends_on_id)) {
                    const activeFromIdx = store.activeWasmGraph.addNode(d.issue_id);
                    const activeToIdx = store.activeWasmGraph.addNode(d.depends_on_id);
                    store.activeWasmGraph.addEdge(activeFromIdx, activeToIdx);
                }
            });

        store.whatIfWasmGraph = buildCompanionWhatIfWasmGraph(DiGraph, resolvedIds);

        console.log(`[bv-graph] WASM graph: ${store.wasmGraph.nodeCount()} nodes, ${store.wasmGraph.edgeCount()} edges (active: ${store.activeWasmGraph.nodeCount()} nodes, ${store.activeWasmGraph.edgeCount()} edges)`);
    } catch (e) {
        console.warn('[bv-graph] Failed to build WASM graph:', e);
        if (store.whatIfWasmGraph) {
            try { store.whatIfWasmGraph.free(); } catch {}
            store.whatIfWasmGraph = null;
        }
        store.whatIfModel = null;
        store.wasmGraph = null;
        store.activeWasmGraph = null;
    }
}

// Keep the viewer's metric names separate from wasm-bindgen's serialized fields.
export function computeHITSMetrics(wasmGraph) {
    const result = wasmGraph.hitsDefault();
    return { hub: result.hubs, authority: result.authorities };
}

function computeMetrics(layout = null) {
    if (!store.wasmReady || !store.wasmGraph) return;

    const start = performance.now();
    const activeGraph = store.activeWasmGraph || store.wasmGraph;

    try {
        const fullNodeCount = store.wasmGraph.nodeCount();
        const activeNodeCount = activeGraph.nodeCount();

        // PageRank (importance)
        if (!store.metrics.pagerank) store.metrics.pagerank = store.wasmGraph.pagerankDefault();

        // Critical path heights (depth on active DAG, unaffected by closed cycles)
        store.metrics.criticalPath = activeGraph.criticalPathHeights();

        // Eigenvector (influence): on DAGs, directed power iteration returns a flat
        // uniform vector (max - min < 1e-9 when len > 1), and core-only WASM builds
        // omit eigenvectorDefault; prefer differentiated Go eigenvector values
        // exported in issue_overview_mv / layout when present.
        let eigScores = typeof store.wasmGraph.eigenvectorDefault === 'function'
            ? store.wasmGraph.eigenvectorDefault()
            : null;
        let needFallbackEig = !eigScores;
        if (eigScores && eigScores.length > 1) {
            let minEv = Infinity;
            let maxEv = -Infinity;
            for (let i = 0; i < eigScores.length; i++) {
                const v = Number(eigScores[i]) || 0;
                if (v < minEv) minEv = v;
                if (v > maxEv) maxEv = v;
            }
            if (maxEv - minEv < 1e-9) {
                needFallbackEig = true;
            }
        }
        if (needFallbackEig) {
            const layoutEig = layout?.centralityValues?.eigenvector || layout?.centrality?.eigenvector || null;
            const fallbackEig = new Float64Array(fullNodeCount);
            let minFb = Infinity;
            let maxFb = -Infinity;
            let hasFb = false;
            for (let i = 0; i < fullNodeCount; i++) {
                const id = store.wasmGraph.nodeId(i);
                const issue = id ? store.nodeMap.get(id) : null;
                const rawVal = (layoutEig && typeof layoutEig === 'object' && Object.hasOwn(layoutEig, id))
                    ? layoutEig[id]
                    : issue?.eigenvector;
                const numVal = Number(rawVal);
                if (rawVal !== undefined && rawVal !== null && Number.isFinite(numVal)) {
                    fallbackEig[i] = numVal;
                    if (numVal < minFb) minFb = numVal;
                    if (numVal > maxFb) maxFb = numVal;
                    hasFb = true;
                }
            }
            if (hasFb && (!eigScores || maxFb - minFb >= 1e-9)) {
                eigScores = fallbackEig;
            }
        }
        store.metrics.eigenvector = eigScores;

        // K-core (cohesion) computed on active subgraph and projected to full graph indices (0 for closed)
        const projectedKCore = new Uint32Array(fullNodeCount);
        if (activeNodeCount > 0 && typeof activeGraph.kcore === 'function') {
            const rawKCore = activeGraph.kcore();
            if (rawKCore) {
                for (let aIdx = 0; aIdx < activeNodeCount; aIdx++) {
                    const id = activeGraph.nodeId(aIdx);
                    const fullIdx = id ? store.wasmGraph.nodeIdx(id) : undefined;
                    if (fullIdx !== undefined && fullIdx < fullNodeCount) {
                        projectedKCore[fullIdx] = rawKCore[aIdx] || 0;
                    }
                }
            }
        }
        store.metrics.kcore = projectedKCore;

        // Betweenness (bottleneck) computed on active subgraph and projected to full graph indices (0 for closed; null when empty)
        if (!store.metrics.betweenness && fullNodeCount > 0) {
            const projectedBetweenness = new Float64Array(fullNodeCount);
            if (activeNodeCount > 0) {
                const rawBetweenness = activeNodeCount > 500
                    ? activeGraph.betweennessApprox(Math.min(100, activeNodeCount))
                    : activeGraph.betweenness();
                if (rawBetweenness) {
                    for (let aIdx = 0; aIdx < activeNodeCount; aIdx++) {
                        const id = activeGraph.nodeId(aIdx);
                        const fullIdx = id ? store.wasmGraph.nodeIdx(id) : undefined;
                        if (fullIdx !== undefined && fullIdx < fullNodeCount) {
                            projectedBetweenness[fullIdx] = rawBetweenness[aIdx] || 0;
                        }
                    }
                }
            }
            store.metrics.betweenness = projectedBetweenness;
        }

        // HITS (hub and authority scores)
        if (typeof store.wasmGraph.hitsDefault === 'function') {
            try {
                store.metrics.hits = computeHITSMetrics(store.wasmGraph);
            } catch (e) {
                console.warn('[bv-graph] HITS computation skipped:', e);
                store.metrics.hits = null;
            }
        } else {
            store.metrics.hits = null;
        }

        // Slack (topological float in hops - only valid on active acyclic DAGs)
        const hasCycles = Boolean(activeGraph.hasCycles());
        store.metrics.hasCycles = hasCycles;
        if (!hasCycles && typeof activeGraph.slack === 'function') {
            try {
                store.metrics.slack = activeGraph.slack();
            } catch (e) {
                console.warn('[bv-graph] Slack computation skipped:', e);
                store.metrics.slack = null;
            }
        } else {
            store.metrics.slack = null;
        }

        // Articulation points (cut vertices on active subgraph - removing them disconnects active work)
        if (typeof activeGraph.articulationPoints === 'function') {
            try {
                const artPoints = activeGraph.articulationPoints();
                if (artPoints && artPoints.length > 0) {
                    store.metrics.articulationPoints = new Set(
                        Array.from(artPoints)
                            .map(idx => activeGraph.nodeId(idx))
                            .filter(Boolean)
                    );
                } else {
                    store.metrics.articulationPoints = new Set();
                }
            } catch (e) {
                console.warn('[bv-graph] Articulation points computation skipped:', e);
                store.metrics.articulationPoints = new Set();
            }
        } else {
            store.metrics.articulationPoints = new Set();
        }

        // Tarjan bridge cut-edges on active subgraph (single-link bottlenecks)
        // Exclude antiparallel 2-node directed cycles (A->B and B->A), which are not single-edge cuts.
        if (typeof activeGraph.bridges === 'function') {
            try {
                const rawBridges = activeGraph.bridges();
                const activeDirEdges = buildActiveBlockingEdgeKeys();
                const bridgeKeys = new Set();
                if (Array.isArray(rawBridges)) {
                    for (const pair of rawBridges) {
                        if (!Array.isArray(pair) || pair.length < 2) continue;
                        const idA = activeGraph.nodeId(pair[0]);
                        const idB = activeGraph.nodeId(pair[1]);
                        if (idA && idB) {
                            const keyAB = edgeKey(idA, idB);
                            const keyBA = edgeKey(idB, idA);
                            if (activeDirEdges.has(keyAB) && activeDirEdges.has(keyBA)) {
                                continue;
                            }
                            bridgeKeys.add(keyAB);
                            bridgeKeys.add(keyBA);
                        }
                    }
                }
                store.metrics.bridges = bridgeKeys;
            } catch (e) {
                console.warn('[bv-graph] Bridges computation skipped:', e);
                store.metrics.bridges = new Set();
            }
        } else {
            store.metrics.bridges = new Set();
        }

        // Cycles (enumerated on active graph so closed historical cycles are ignored)
        const cycleResult = activeGraph.enumerateCycles(100);
        store.metrics.cycles = cycleResult;

        // Cycle-break edge recommendations (active subgraph only)
        if (hasCycles && typeof activeGraph.cycleBreakSuggestions === 'function') {
            try {
                const maxBreakCandidates = Math.max(100, typeof activeGraph.edgeCount === 'function' ? activeGraph.edgeCount() : 100);
                const rawBreak = activeGraph.cycleBreakSuggestions(maxBreakCandidates, 100);
                const list = rawBreak?.suggestions || rawBreak?.top_suggestions || [];
                const resolved = [];
                if (Array.isArray(list)) {
                    for (const item of list) {
                        if (!item) continue;
                        const from = item.from_id || (typeof item.from === 'number' ? activeGraph.nodeId(item.from) : item.from);
                        const to = item.to_id || (typeof item.to === 'number' ? activeGraph.nodeId(item.to) : item.to);
                        if (from && to) {
                            resolved.push({
                                from: String(from),
                                to: String(to),
                                edgeKey: edgeKey(String(from), String(to)),
                                cyclesBroken: Number(item.cycles_broken ?? item.cyclesBroken ?? 1) || 1
                            });
                        }
                    }
                }
                store.metrics.cycleBreakSuggestions = resolved;
            } catch (e) {
                console.warn('[bv-graph] Cycle break suggestions skipped:', e);
                store.metrics.cycleBreakSuggestions = [];
            }
        } else {
            store.metrics.cycleBreakSuggestions = [];
        }

        const elapsed = performance.now() - start;
        console.log(`[bv-graph] Metrics computed in ${elapsed.toFixed(1)}ms`);
    } catch (e) {
        console.warn('[bv-graph] Metric computation failed:', e);
    }
}

// ============================================================================
// GRAPH INITIALIZATION
// ============================================================================

export async function initGraph(containerId, options = {}) {
    store.container = document.getElementById(containerId);
    if (!store.container) {
        throw new Error(`Container '${containerId}' not found`);
    }

    // Merge config
    Object.assign(store.config, options);

    // Clear container and ensure relative positioning for scoped overlays
    store.container.innerHTML = '';
    if (!store.container.style.position) {
        store.container.style.position = 'relative';
    }
    tooltipEl = null;
    labelLegendEl = null;

    const initialDark = typeof options.darkMode === 'boolean'
        ? options.darkMode
        : (typeof document !== 'undefined' ? document.documentElement.classList.contains('dark') : true);
    applyThemePalette(initialDark);

    // Initialize WASM
    await initWasm();

    // Create force-graph instance
    store.graph = ForceGraph()(store.container)
        // Data binding
        .nodeId('id')
        .linkSource('source')
        .linkTarget('target')

        // Node rendering
        .nodeCanvasObject(drawNode)
        .nodeCanvasObjectMode(() => 'replace')
        .nodePointerAreaPaint(drawNodeHitArea)

        // Link rendering
        .linkCanvasObject(drawLink)
        .linkCanvasObjectMode(() => 'replace')
        .linkDirectionalParticles(link => (store.config.enableParticles && link.blocking !== false) ? 2 : 0)
        .linkDirectionalParticleSpeed(store.config.particleSpeed)
        .linkDirectionalParticleColor(() => THEME.accent.cyan)

        // Forces
        .d3Force('charge', d3.forceManyBody()
            .strength(store.config.chargeStrength)
            .distanceMax(300))
        .d3Force('link', d3.forceLink()
            .distance(link => getLinkDistance(link))
            .strength(0.7))
        .d3Force('center', d3.forceCenter())
        .d3Force('x', d3.forceX()
            .strength(store.config.centerStrength))
        .d3Force('y', d3.forceY()
            .strength(store.config.centerStrength))
        .d3Force('collision', d3.forceCollide()
            .radius(node => getNodeSize(node) + 5))

        // Warmup
        .warmupTicks(store.config.warmupTicks)
        .cooldownTicks(store.config.cooldownTicks)

        // Interaction handlers
        .onNodeClick(handleNodeClick)
        .onNodeRightClick(handleNodeRightClick)
        .onNodeHover(handleNodeHover)
        .onNodeDrag(handleNodeDrag)
        .onNodeDragEnd(handleNodeDragEnd)
        .onLinkClick(handleLinkClick)
        .onLinkHover(handleLinkHover)
        .onBackgroundClick(handleBackgroundClick)
        .onZoom(handleZoom)

        // Simulation progress tracking with early "done" detection
        // The simulation can run for a long time at very low alpha values,
        // but the layout is visually stable much earlier. We consider it "done"
        // when alpha drops below 0.05 (95% progress) for smoother UX.
        .onEngineTick(() => {
            // Get current simulation alpha (1 = start, 0 = done)
            // Alpha decays from 1 towards alphaMin (0.001)
            const alpha = store.graph.d3Alpha?.() ?? 0;
            const progress = Math.round((1 - alpha) * 100);

            // Consider layout stable when alpha < 0.05 (95% progress)
            // This provides a much snappier feel without waiting for full convergence
            const effectivelyDone = alpha < 0.05;
            dispatchEvent('simulationProgress', {
                alpha,
                progress: effectivelyDone ? 100 : progress,
                done: effectivelyDone
            });
        })
        .onEngineStop(() => {
            dispatchEvent('simulationProgress', { alpha: 0, progress: 100, done: true });
        })

        // Background
        .backgroundColor(THEME.bg)

        // Custom rendering for cluster hulls (bv-qpt0)
        .onRenderFramePre((ctx, globalScale) => {
            if (labelClusterState.active && labelClusterState.showHulls) {
                drawClusterHulls(ctx, globalScale);
            }
        })
        .onRenderFramePost((ctx, globalScale) => {
            if (!timeTravelState.active) return;
            for (const node of timeTravelState.originalNodes) {
                const state = timeTravelState.nodeStates.get(node.id);
                if (state && !state.visible && timelineOpacity(state) > 0) {
                    drawNode(node, ctx, globalScale);
                }
            }
        });

    // Follow actual container dimensions, including the detail pane's CSS
    // transition and viewport changes, rather than racing a fixed timeout.
    store.resizeObserver?.disconnect();
    const graph = store.graph;
    const container = store.container;
    store.resizeObserver = new ResizeObserver(() => {
        if (store.graph !== graph) return;
        const width = container.clientWidth;
        const height = container.clientHeight;
        if (width > 0 && height > 0) {
            if (graph.width() !== width) graph.width(width);
            if (graph.height() !== height) graph.height(height);
        }
    });
    store.resizeObserver.observe(container);

    // Setup keyboard shortcuts
    setupKeyboardShortcuts();

    // Emit ready event
    dispatchEvent('ready', { graph: store.graph, wasmReady: store.wasmReady });

    return store.graph;
}

// ============================================================================
// DATA LOADING
// ============================================================================

/**
 * Load optional starting coordinates. Only topology affects these coordinates,
 * so compare every node and directed blocking edge with the loaded database.
 * Reuse only complete, validated centrality snapshots. Legacy metric tuples
 * lack computation status and are never trusted as a computation result.
 * @returns {Promise<object|null>} Layout data or null if not available
 */
export async function loadPrecomputedLayout(issues, dependencies) {
    try {
        const meta = typeof window !== 'undefined'
            ? (window.BV_META || window.beadsViewer?.getMeta?.() || null)
            : null;
        const generatedRaw = meta?.generated_at ?? meta?.exported_at ?? meta?.created_at ?? meta?.timestamp;
        const parsedTs = generatedRaw ? Date.parse(String(generatedRaw)) : NaN;
        const cacheToken = Number.isFinite(parsedTs) && parsedTs > 0 ? parsedTs : Date.now();
        const response = await fetch(`data/graph_layout.json?_t=${cacheToken}`, { signal: AbortSignal.timeout(3000) });
        if (!response.ok) return null;
        return validateLayout(await response.json(), issues, dependencies);
    } catch (e) {
        console.log('[bv-graph] No pre-computed layout, will use dynamic simulation');
        return null;
    }
}

function validateLayout(layout, issues, dependencies) {
    if (!layout || !Array.isArray(issues) || !Array.isArray(dependencies)) return null;
    const positions = layout.positions;
    const ids = new Set(issues.map(issue => issue.id));
    if (!positions || typeof positions !== 'object' || Array.isArray(positions)
        || ids.size !== issues.length || layout.node_count !== ids.size
        || Object.keys(positions).length !== ids.size) return null;
    for (const id of ids) {
        const pos = Object.hasOwn(positions, id) ? positions[id] : null;
        if (!Array.isArray(pos) || pos.length !== 2 || !pos.every(Number.isFinite)) return null;
    }
    const edges = dependencies.filter(isBlockingDependency)
        .map(dep => JSON.stringify([dep.depends_on_id, dep.issue_id])).sort();
    if (!Array.isArray(layout.links) || layout.edge_count !== edges.length
        || layout.links.length !== edges.length) return null;
    const links = [];
    for (const link of layout.links) {
        if (!Array.isArray(link) || link.length !== 2
            || !link.every(id => typeof id === 'string')) return null;
        links.push(JSON.stringify(link));
    }
    links.sort();
    if (links.some((link, i) => link !== edges[i])) return null;
    const nodeIDs = new Set(ids);
    for (const dependency of dependencies.filter(isBlockingDependency)) {
        nodeIDs.add(dependency.issue_id);
        nodeIDs.add(dependency.depends_on_id);
    }
    const centrality = {};
    const snapshot = layout.centrality;
    if (snapshot?.version === 1) {
        for (const [name, statusName] of [['pagerank', 'PageRank'], ['betweenness', 'Betweenness']]) {
            const values = snapshot[name];
            const status = snapshot.status?.[statusName];
            if (status?.state !== 'computed' || !values || typeof values !== 'object' || Array.isArray(values)
                || Object.keys(values).length !== nodeIDs.size) continue;
            // Native and browser sampling use different pivots and thresholds.
            // Only exact betweenness has the same contract in both engines.
            if (name === 'betweenness' && status.reason === 'approximate') continue;
            const bound = name === 'pagerank' ? 1 : nodeIDs.size * nodeIDs.size;
            if (![...nodeIDs].every(id => Object.hasOwn(values, id) && Number.isFinite(values[id]) && values[id] >= 0 && values[id] <= bound)) continue;
            if (name === 'pagerank' && nodeIDs.size && Math.abs(Object.values(values).reduce((sum, n) => sum + n, 0) - 1) > 0.0001) continue;
            centrality[name] = values;
        }
    }
    return { positions, links: layout.links, node_count: layout.node_count,
        edge_count: layout.edge_count, centrality: snapshot, centralityValues: centrality };
}

function computeGraphFallbackCycles(issues, dependencies, maxCycles = 100) {
    const activeIssues = (issues || []).filter(issue => issue && !isResolvedIssue(issue));
    const sortedIds = [...new Set(activeIssues.map(i => i.id).filter(Boolean))].sort();
    const n = sortedIds.length;
    if (n === 0 || !Array.isArray(dependencies) || dependencies.length === 0) {
        return { sccComponents: [], cycles: [], truncated: false };
    }
    const idToIdx = new Map(sortedIds.map((id, i) => [id, i]));
    const adjSets = Array.from({ length: n }, () => new Set());
    const selfLoopSet = new Set();
    for (const d of dependencies) {
        if (!isBlockingDependency(d)) continue;
        const u = idToIdx.get(d.issue_id);
        const v = idToIdx.get(d.depends_on_id);
        if (u === undefined || v === undefined) continue;
        adjSets[u].add(v);
        if (u === v) selfLoopSet.add(u);
    }
    const adj = adjSets.map(s => [...s].sort((a, b) => a - b));

    const disc = new Int32Array(n).fill(-1);
    const low = new Int32Array(n);
    const onStack = new Uint8Array(n);
    const tarjanStack = [];
    const callStack = [];
    const nonTrivialSccs = [];
    let timer = 0;

    for (let root = 0; root < n; root++) {
        if (disc[root] !== -1) continue;
        disc[root] = low[root] = timer++;
        onStack[root] = 1;
        tarjanStack.push(root);
        callStack.push({ v: root, ni: 0 });

        while (callStack.length > 0) {
            const frame = callStack[callStack.length - 1];
            const v = frame.v;
            const nbrs = adj[v];
            if (frame.ni < nbrs.length) {
                const w = nbrs[frame.ni++];
                if (disc[w] === -1) {
                    disc[w] = low[w] = timer++;
                    onStack[w] = 1;
                    tarjanStack.push(w);
                    callStack.push({ v: w, ni: 0 });
                } else if (onStack[w] && disc[w] < low[v]) {
                    low[v] = disc[w];
                }
            } else {
                callStack.pop();
                if (callStack.length > 0) {
                    const parent = callStack[callStack.length - 1].v;
                    if (low[v] < low[parent]) low[parent] = low[v];
                }
                if (low[v] === disc[v]) {
                    const comp = [];
                    while (true) {
                        const w = tarjanStack.pop();
                        onStack[w] = 0;
                        comp.push(w);
                        if (w === v) break;
                    }
                    comp.sort((a, b) => a - b);
                    if (comp.length > 1 || (comp.length === 1 && selfLoopSet.has(comp[0]))) {
                        nonTrivialSccs.push(comp);
                    }
                }
            }
        }
    }
    nonTrivialSccs.sort((a, b) => a[0] - b[0]);

    const cycles = [];
    let truncated = false;
    for (const comp of nonTrivialSccs) {
        if (cycles.length >= maxCycles) {
            truncated = true;
            break;
        }
        if (comp.length === 1) {
            cycles.push([sortedIds[comp[0]]]);
            continue;
        }
        const compSet = new Set(comp);
        if (comp.length <= 256) {
            for (let sPos = 0; sPos < comp.length; sPos++) {
                if (cycles.length >= maxCycles) {
                    truncated = true;
                    break;
                }
                const start = comp[sPos];
                if (selfLoopSet.has(start)) {
                    cycles.push([sortedIds[start]]);
                    if (cycles.length >= maxCycles) {
                        truncated = true;
                        break;
                    }
                }
                const blocked = new Set();
                const blockMap = new Map();
                const path = [];
                const unblock = (u) => {
                    blocked.delete(u);
                    const waiting = blockMap.get(u);
                    if (waiting) {
                        blockMap.delete(u);
                        for (const w of waiting) {
                            if (blocked.has(w)) unblock(w);
                        }
                    }
                };
                const circuit = (v) => {
                    let foundCycle = false;
                    path.push(v);
                    blocked.add(v);
                    for (const w of adj[v]) {
                        if (w < start || !compSet.has(w)) continue;
                        if (w === start) {
                            if (path.length > 1) {
                                cycles.push(path.map(idx => sortedIds[idx]));
                                foundCycle = true;
                                if (cycles.length >= maxCycles) {
                                    truncated = true;
                                    path.pop();
                                    return true;
                                }
                            }
                        } else if (!blocked.has(w)) {
                            if (circuit(w)) foundCycle = true;
                            if (truncated) {
                                path.pop();
                                return true;
                            }
                        }
                    }
                    if (foundCycle) {
                        unblock(v);
                    } else {
                        for (const w of adj[v]) {
                            if (w < start || !compSet.has(w)) continue;
                            let set = blockMap.get(w);
                            if (!set) {
                                set = new Set();
                                blockMap.set(w, set);
                            }
                            set.add(v);
                        }
                    }
                    path.pop();
                    return foundCycle;
                };
                circuit(start);
            }
        } else {
            const covered = new Set();
            for (const start of comp) {
                if (cycles.length >= maxCycles) {
                    truncated = true;
                    break;
                }
                if (covered.has(start)) continue;
                const parent = new Map();
                const queue = [];
                let foundEnd = -1;
                for (const nb of adj[start]) {
                    if (!compSet.has(nb)) continue;
                    if (nb === start) {
                        foundEnd = start;
                        break;
                    }
                    parent.set(nb, start);
                    queue.push(nb);
                }
                let head = 0;
                while (foundEnd === -1 && head < queue.length) {
                    const cur = queue[head++];
                    for (const nb of adj[cur]) {
                        if (!compSet.has(nb)) continue;
                        if (nb === start) {
                            foundEnd = cur;
                            break;
                        }
                        if (!parent.has(nb)) {
                            parent.set(nb, cur);
                            queue.push(nb);
                        }
                    }
                }
                if (foundEnd !== -1) {
                    const rev = [foundEnd];
                    let p = foundEnd;
                    while (p !== start) {
                        p = parent.get(p);
                        rev.push(p);
                    }
                    rev.reverse();
                    rev.forEach(idx => covered.add(idx));
                    cycles.push(rev.map(idx => sortedIds[idx]));
                }
            }
        }
    }

    return {
        sccComponents: nonTrivialSccs.map(comp => comp.map(idx => sortedIds[idx])),
        cycles,
        truncated
    };
}

function resolveCycleNodeId(idx, activeGraph) {
    if (activeGraph && typeof activeGraph.nodeId === 'function') {
        return activeGraph.nodeId(idx);
    }
    return store.nodeIdByIndex?.[idx];
}

function edgeKey(from, to) {
    return `${from}\0${to}`;
}

function buildActiveBlockingEdgeKeys() {
    const keys = new Set();
    for (const d of (store.dependencies || [])) {
        if (!isBlockingDependency(d)) continue;
        const srcIssue = store.nodeMap.get(d.issue_id);
        const tgtIssue = store.nodeMap.get(d.depends_on_id);
        if ((srcIssue && isResolvedIssue(srcIssue)) || (tgtIssue && isResolvedIssue(tgtIssue))) continue;
        keys.add(edgeKey(d.issue_id, d.depends_on_id));
    }
    return keys;
}

/**
 * Load data with optional starting coordinates and live force simulation.
 * @param {Array} issues - Issue objects from SQLite
 * @param {Array} dependencies - Dependency objects from SQLite
 * @param {object} [layout] - Optional pre-computed layout
 */
export function loadData(issues, dependencies, layout = null) {
    resetTimeTravel();
    resetWhatIf({ force: true });
    resetCriticalPath();
    resetCycleNavigator();
    resetDependencyCone();
    if (labelClusterState.active) {
        deactivateLabelGalaxy();
    }
    store.reset();
    dispatchEvent('heatmapToggle', { active: false, metric: store.sizeMetric });
    store.issues = issues;
    store.dependencies = dependencies;
    // Public callers receive the same topology/status validation as fetches.
    layout = layout ? validateLayout(layout, issues, dependencies) : null;

    // Build lookup maps
    issues.forEach((issue, idx) => {
        store.nodeMap.set(issue.id, issue);
        store.nodeIndexMap.set(issue.id, idx);
        store.nodeIdByIndex.push(issue.id);
    });

    const precomputedMetrics = [];
    // Reuse validated centrality, while retaining the live graph and computing
    // browser-specific metrics such as critical path and cycle enumeration.
    if (store.wasmReady) {
        buildWasmGraph();
        const centrality = layout?.centralityValues;
        const hasClosedGraphNodes = Boolean(
            store.activeWasmGraph &&
            store.wasmGraph &&
            store.activeWasmGraph.nodeCount() < store.wasmGraph.nodeCount()
        );
        if (store.wasmGraph && centrality) {
            for (const name of ['pagerank', 'betweenness']) {
                if (name === 'betweenness' && hasClosedGraphNodes) continue;
                if (centrality[name]) {
                    store.metrics[name] = Array.from({length: store.wasmGraph.nodeCount()}, (_, i) => centrality[name][store.wasmGraph.nodeId(i)]);
                    precomputedMetrics.push(name);
                }
            }
        }
        computeMetrics(layout);
    } else if (issues.length > 0) {
        const centrality = layout?.centralityValues;
        const hasClosedIssues = issues.some(isResolvedIssue);
        const prVec = new Float64Array(issues.length);
        const bwVec = new Float64Array(issues.length);
        const evVec = new Float64Array(issues.length);
        const cpVec = new Float64Array(issues.length);
        const standaloneFlaggedIndices = [];
        issues.forEach((issue, i) => {
            const prVal = centrality?.pagerank?.[issue.id] ?? Number(issue.pagerank);
            prVec[i] = Number.isFinite(prVal) ? prVal : 0;
            const bwRaw = (!hasClosedIssues && centrality?.betweenness?.[issue.id] !== undefined)
                ? centrality.betweenness[issue.id]
                : Number(issue.betweenness);
            bwVec[i] = (!isResolvedIssue(issue) && Number.isFinite(bwRaw)) ? bwRaw : 0;
            const evVal = Number(issue.eigenvector);
            evVec[i] = Number.isFinite(evVal) ? evVal : 0;
            const cpVal = Number(issue.critical_path_depth ?? issue.critical_depth);
            cpVec[i] = Number.isFinite(cpVal) ? cpVal : 0;
            if (!isResolvedIssue(issue) && Boolean(issue.in_cycle)) {
                standaloneFlaggedIndices.push(i);
            }
        });
        const fbCycles = computeGraphFallbackCycles(issues, dependencies, 100);
        const indexCycles = fbCycles.cycles.length > 0
            ? fbCycles.cycles
                .map(cyc => cyc.map(id => store.nodeIndexMap.get(id)).filter(idx => idx !== undefined))
                .filter(cyc => cyc.length > 0)
            : standaloneFlaggedIndices.map(idx => [idx]);
        const hasAnyCycles = indexCycles.length > 0;
        store.metrics.pagerank = prVec;
        store.metrics.betweenness = bwVec;
        store.metrics.eigenvector = evVec;
        store.metrics.criticalPath = cpVec;
        store.metrics.hasCycles = hasAnyCycles;
        store.metrics.cycles = {
            has_cycles: hasAnyCycles,
            count: indexCycles.length,
            truncated: Boolean(fbCycles.truncated),
            scc_components: fbCycles.sccComponents,
            cycles: indexCycles
        };
    }

    // Build label color map for galaxy view
    buildLabelColorMap();

    // Prepare graph data with optional pre-computed positions
    const graphData = prepareGraphData(layout);

    // Pre-populate maxMetrics from prepared nodes before warmupTicks runs
    computeMaxMetrics(graphData.nodes);

    // Update graph
    // Valid starting coordinates make synchronous warmup redundant. Paint the
    // seeded graph immediately and let subsequent frames refine the layout.
    store.graph.warmupTicks(layout ? 0 : store.config.warmupTicks);
    store.graph.graphData(graphData);

    // Compute max metric values for heatmap normalization (after graph data is set)
    computeMaxMetrics();

    // Fit immediately if pre-computed, otherwise wait for simulation
    if (layout?.positions) {
        store.graph.zoomToFit(200, 50);
    } else {
        const graph = store.graph;
        setTimeout(() => {
            if (store.graph === graph) graph.zoomToFit(400, 50);
        }, 500);
    }

    // Emit event
    dispatchEvent('dataLoaded', {
        nodeCount: graphData.nodes.length,
        linkCount: graphData.links.length,
        metrics: store.metrics,
        precomputedMetrics,
        precomputed: !!layout
    });

    return graphData;
}

function prepareGraphData(layout = null) {
    const { issues, dependencies, filters, metrics } = store;

    // Filter nodes
    let nodes = issues.filter(issue => {
        // Status filter (treat dependency-blocked issues as blocked and tombstones as closed)
        if (filters.status) {
            if (filters.status === 'blocked') {
                const isDepBlocked = !isResolvedIssue(issue) && (
                    (Number(issue.blocker_count) || 0) > 0 ||
                    (issue.dependency_state && issue.dependency_state !== 'satisfied')
                );
                if (issue.status !== 'blocked' && !isDepBlocked) return false;
            } else if (filters.status === 'closed') {
                if (!isResolvedIssue(issue)) return false;
            } else if (issue.status !== filters.status) {
                return false;
            }
        }
        if (!filters.showClosed && isResolvedIssue(issue)) return false;

        // Priority filter
        if (filters.priority !== null && issue.priority !== filters.priority) return false;

        // Label filter
        if (filters.labels.length > 0) {
            const issueLabels = issue.labels || [];
            if (!filters.labels.some(l => issueLabels.includes(l))) return false;
        }

        // Source repo filter
        if (filters.repo && (issue.source_repo || '') !== filters.repo) return false;

        // Search filter
        const term = String(filters.search || '').trim().toLowerCase();
        if (term) {
            const searchable = `${issue.id} ${issue.title} ${issue.description || ''} ${issue.source_repo || ''}`.toLowerCase();
            if (!searchable.includes(term)) return false;
        }

        return true;
    });

    // Build node set for link filtering
    const nodeIds = new Set(nodes.map(n => n.id));

    // Filter links (include renderable non-blocking edges visually; mark blocking & bridge flags)
    let links = dependencies
        .filter(isRenderableDependency)
        .filter(d => nodeIds.has(d.issue_id) && nodeIds.has(d.depends_on_id))
        .map(d => {
            const blocking = isBlockingDependency(d);
            return {
                source: d.issue_id,
                target: d.depends_on_id,
                type: d.type || 'blocks',
                blocking,
                isBridge: blocking && Boolean(metrics.bridges?.has(edgeKey(d.issue_id, d.depends_on_id)))
            };
        });

    // Enrich nodes with computed data
    const activeGraph = store.activeWasmGraph || store.wasmGraph;
    nodes = nodes.map(issue => {
        const idx = store.wasmReady
            ? store.wasmGraph?.nodeIdx(issue.id)
            : store.nodeIndexMap.get(issue.id);
        const activeIdx = store.wasmReady
            ? activeGraph?.nodeIdx(issue.id)
            : store.nodeIndexMap.get(issue.id);
        const pos = layout?.positions?.[issue.id]; // Pre-computed position
        const blockerCount = issue.blocker_count
            ?? dependencies.filter(d => isBlockingDependency(d) && d.issue_id === issue.id).length;
        const dependentCount = issue.dependent_count
            ?? dependencies.filter(d => isBlockingDependency(d) && d.depends_on_id === issue.id).length;

        return {
            id: issue.id,
            title: issue.title,
            description: issue.description,
            status: issue.status || 'open',
            priority: issue.priority ?? 2,
            type: issue.type || 'task',
            labels: issue.labels || [],
            assignee: issue.assignee,
            sourceRepo: issue.source_repo || '',
            createdAt: issue.created_at,
            updatedAt: issue.updated_at,
            triageScore: issue.triage_score ?? null,
            estimatedMinutes: issue.estimated_minutes ?? null,
            dueDate: issue.due_date || null,
            eta_days: issue.eta_days ?? null,
            eta_date: issue.eta_date || null,
            eta_confidence: issue.eta_confidence ?? null,
            risk_score: Number.isFinite(Number(issue.risk_score)) ? Number(issue.risk_score) : 0,
            risk_explanation: issue.risk_explanation || '',

            // Metrics computed on the currently loaded graph.
            pagerank: idx !== undefined && metrics.pagerank ? metrics.pagerank[idx] : 0,
            betweenness: idx !== undefined && metrics.betweenness ? metrics.betweenness[idx] : 0,
            criticalDepth: activeIdx !== undefined && metrics.criticalPath ? metrics.criticalPath[activeIdx] : 0,
            eigenvector: idx !== undefined && metrics.eigenvector ? metrics.eigenvector[idx] : 0,
            kcore: idx !== undefined && metrics.kcore ? metrics.kcore[idx] : 0,
            slack: activeIdx !== undefined ? (metrics.slack ? (metrics.slack[activeIdx] ?? null) : null) : null,
            hitsHub: idx !== undefined && metrics.hits?.hub ? metrics.hits.hub[idx] : undefined,
            hitsAuth: idx !== undefined && metrics.hits?.authority ? metrics.hits.authority[idx] : undefined,
            inDegree: idx !== undefined && store.wasmGraph ? store.wasmGraph.inDegree(idx) : dependentCount,
            outDegree: idx !== undefined && store.wasmGraph ? store.wasmGraph.outDegree(idx) : blockerCount,
            isArticulationPoint: !isResolvedIssue(issue) && Boolean(metrics.articulationPoints?.has(issue.id)),
            inCycle: false,

            // Dependency counts. Prefer the active-blocker count from the
            // materialized view (issue.blocker_count / dependent_count) so
            // closed blockers don't keep their dependents looking blocked
            // (bv-issue#143/#144). Fall back to blocking dependency-row counts.
            blockerCount,
            dependentCount,
            dependencyState: issue.dependency_state || 'satisfied',
            isActionable: issue.is_actionable !== undefined ? Boolean(issue.is_actionable) : blockerCount === 0,

            // Seed the simulation without pinning nodes or preventing dragging.
            x: pos ? pos[0] : undefined,
            y: pos ? pos[1] : undefined,
            fx: null,
            fy: null
        };
    });

    // Mark active cycle nodes from the current active graph.
    if (metrics.cycles?.cycles) {
        const cycleNodes = new Set(metrics.cycles.cycles.flat());
        nodes.forEach(node => {
            const activeIdx = store.wasmReady
                ? activeGraph?.nodeIdx(node.id)
                : store.nodeIndexMap.get(node.id);
            node.inCycle = activeIdx !== undefined && cycleNodes.has(activeIdx);
        });
    }

    return { nodes, links };
}

// ============================================================================
// NODE RENDERING
// ============================================================================

export function getNodeSize(node) {
    const { nodeMinSize, nodeMaxSize } = store.config;
    const key = normalizeMetricKey(store.sizeMetric);
    let score = getMetricRatio(node, key, false);

    // Boost for high betweenness (bottleneck nodes) in default PageRank mode
    if (key === 'pagerank' && (node.betweenness || 0) > 0.1) {
        score = Math.min(1, score + 0.1);
    }

    return nodeMinSize + score * (nodeMaxSize - nodeMinSize);
}

export function getNodeColor(node) {
    // What-if simulation states take priority
    if (node._whatIfState === 'closing') return THEME.accent.green;
    if (node._whatIfState === 'unblocked') return THEME.accent.cyan;

    // Critical path nodes get red/orange highlight
    if (node._criticalPathState === 'active') return THEME.accent.red;

    // Cycle navigator highlight (current cycle)
    if (node._cycleHighlight) return THEME.accent.red;

    // Cycle nodes get special color (all cycles, dimmer)
    if (node.inCycle) return THEME.accent.pink;

    // Dependency cone focal node stands out in purple, cone members in cyan
    if (node._coneFocus) return THEME.accent.purple;

    // Highlighted nodes
    if (store.highlightedNodes.has(node.id)) return THEME.accent.cyan;

    // Selected node
    if (store.selectedNode?.id === node.id) return THEME.accent.purple;

    // Heatmap mode: color by metric intensity
    if (store.heatmapMode) {
        return getHeatmapColor(node);
    }

    // Label galaxy mode: color by label
    if (labelClusterState.active) {
        return getLabelColor(node);
    }

    // Status-based color, with derived "blocked" semantics so the graph
    // matches the issues view: an open / in-progress node with at least one
    // active blocker or non-satisfied dependency_state is treated as blocked.
    const depState = node.dependencyState ?? node.dependency_state;
    const blockers = node.blockerCount ?? node.blocker_count ?? 0;
    if (
        (blockers > 0 || (depState && depState !== 'satisfied'))
        && (node.status === 'open' || node.status === 'in_progress')
    ) {
        return THEME.status.blocked;
    }
    return THEME.status[node.status] || THEME.status.open;
}

function getNodeOpacity(node) {
    // Dim non-highlighted nodes when we have highlights
    if (store.highlightedNodes.size > 0 && !store.highlightedNodes.has(node.id)) {
        return 0.3;
    }

    // Dim non-connected nodes when showing gold glow connected subgraph
    if (store.connectedNodes.size > 0 && !store.connectedNodes.has(node.id)) {
        return 0.2;
    }

    // Dim closed nodes
    if (isResolvedIssue(node)) return 0.6;

    return 1;
}

function drawNode(node, ctx, globalScale) {
    const transition = timeTravelState.active ? timeTravelState.nodeStates.get(node.id) : null;
    const fade = transition ? timelineOpacity(transition) : 1;
    if (fade <= 0) return;
    const pulse = transition?.visible && fade < 1 ? 0.35 * Math.sin(Math.PI * fade) : 0;
    const size = getNodeSize(node) * (0.2 + 0.8 * fade + pulse);
    const color = getNodeColor(node);
    const opacity = getNodeOpacity(node) * fade;
    const isHovered = store.hoveredNode?.id === node.id;
    const isSelected = store.selectedNode?.id === node.id;
    const isInConnectedSubgraph = store.connectedNodes.has(node.id);

    ctx.save();
    ctx.globalAlpha = opacity;

    // Golden glow for connected subgraph (when hovering/selecting a node)
    if (store.connectedNodes.size > 0 && isInConnectedSubgraph) {
        ctx.beginPath();
        ctx.arc(node.x, node.y, size + 8, 0, Math.PI * 2);
        const goldGradient = ctx.createRadialGradient(node.x, node.y, size, node.x, node.y, size + 12);
        goldGradient.addColorStop(0, THEME.link.goldGlow);
        goldGradient.addColorStop(1, 'transparent');
        ctx.fillStyle = goldGradient;
        ctx.fill();
    }

    // Enhanced glow for what-if states
    if (node._whatIfState === 'closing') {
        ctx.shadowColor = THEME.accent.green;
        ctx.shadowBlur = 25;
    } else if (node._whatIfState === 'unblocked') {
        ctx.shadowColor = THEME.accent.cyan;
        ctx.shadowBlur = 20;
    }
    // Critical path glow
    else if (node._criticalPathState === 'active') {
        ctx.shadowColor = THEME.accent.red;
        ctx.shadowBlur = 22;
    }
    // Cycle navigator glow (highlighted cycle)
    else if (node._cycleHighlight) {
        ctx.shadowColor = THEME.accent.red;
        ctx.shadowBlur = 25;
    }
    // Cycle member glow (any cycle)
    else if (node.inCycle) {
        ctx.shadowColor = THEME.accent.pink;
        ctx.shadowBlur = 15;
    }
    // Gold glow for connected subgraph nodes
    else if (isInConnectedSubgraph && store.connectedNodes.size > 0) {
        ctx.shadowColor = THEME.accent.gold;
        ctx.shadowBlur = 15;
    }
    // Glow effect for important nodes (PageRank sums to 1.0, so threshold ~2x average)
    else if (node.pagerank > 0.03 || isHovered || isSelected) {
        ctx.shadowColor = color;
        ctx.shadowBlur = isHovered ? 20 : 10;
    }

    // Node body
    ctx.beginPath();
    ctx.arc(node.x, node.y, size, 0, Math.PI * 2);
    ctx.fillStyle = color;
    ctx.fill();

    // Border
    ctx.strokeStyle = isSelected ? THEME.accent.purple :
                      isHovered ? THEME.fg :
                      THEME.bgSecondary;
    ctx.lineWidth = isSelected ? 3 : isHovered ? 2 : 1;
    ctx.stroke();

    // Articulation-point (cut-vertex) halo ring on active subgraph
    if (node.isArticulationPoint) {
        ctx.save();
        ctx.shadowBlur = 0;
        ctx.beginPath();
        ctx.arc(node.x, node.y, size + Math.max(2.5, 3 / globalScale), 0, Math.PI * 2);
        ctx.strokeStyle = THEME.accent.orange || '#FFB86C';
        ctx.lineWidth = Math.max(1.2, 1.5 / globalScale);
        ctx.setLineDash([3 / globalScale, 2 / globalScale]);
        ctx.stroke();
        ctx.restore();
    }

    // Priority indicator (flame for P0/P1)
    if (node.priority <= 1 && globalScale > 0.4) {
        // Keep emoji ~12px on screen
        const emojiSize = Math.min(10, Math.max(4, 12 / globalScale));
        ctx.font = `${emojiSize}px sans-serif`;
        ctx.textAlign = 'center';
        ctx.textBaseline = 'bottom';
        ctx.shadowBlur = 0;
        ctx.fillText(node.priority === 0 ? '\uD83D\uDD25\uD83D\uDD25' : '\uD83D\uDD25', node.x, node.y - size - 2);
    }

    // Cycle warning
    if (node.inCycle && globalScale > 0.4) {
        // Keep emoji ~10px on screen
        const emojiSize = Math.min(8, Math.max(3, 10 / globalScale));
        ctx.font = `${emojiSize}px sans-serif`;
        ctx.textAlign = 'center';
        ctx.textBaseline = 'top';
        ctx.fillText('\u26A0\uFE0F', node.x, node.y + size + 2);
    }

    // Label (when zoomed in)
    if (store.config.showLabels && globalScale > store.config.labelZoomThreshold) {
        // Font should be ~10px on SCREEN regardless of zoom
        // worldFontSize * globalScale ≈ 10-12px
        // Clamp world coords: min 3px (readable at high zoom), max 8px (not huge when zoomed out)
        const fontSize = Math.min(8, Math.max(3, 10 / globalScale));
        ctx.font = `500 ${fontSize}px 'Inter', 'JetBrains Mono', sans-serif`;
        ctx.textAlign = 'center';
        ctx.textBaseline = 'top';
        ctx.shadowBlur = 0;
        ctx.fillStyle = THEME.fg;
        ctx.globalAlpha = opacity * 0.85;

        // Show title when zoomed in enough for it to be readable
        let label = node.id;
        if (globalScale > 1.5) {
            label = truncate(node.title || node.id, 30);
        } else if (globalScale > 1.0) {
            label = truncate(node.title || node.id, 20);
        }
        ctx.fillText(label, node.x, node.y + size + 3);
    }

    ctx.restore();
}

function drawNodeHitArea(node, color, ctx) {
    const size = getNodeSize(node) + 5; // Slightly larger hit area
    ctx.fillStyle = color;
    ctx.beginPath();
    ctx.arc(node.x, node.y, size, 0, Math.PI * 2);
    ctx.fill();
}

// ============================================================================
// LINK RENDERING
// ============================================================================

function getLinkDistance(link) {
    // Shorter distance for critical path links
    const sourceNode = typeof link.source === 'object' ? link.source : store.nodeMap.get(link.source);
    const targetNode = typeof link.target === 'object' ? link.target : store.nodeMap.get(link.target);

    if (sourceNode?.criticalDepth > 0 && targetNode?.criticalDepth > 0) {
        return store.config.linkDistance * 0.7;
    }
    return store.config.linkDistance;
}

function getLinkColor(link) {
    const linkId = edgeKey(link.source?.id || link.source, link.target?.id || link.target);
    const sourceNode = typeof link.source === 'object' ? link.source : store.nodeMap.get(link.source);
    const targetNode = typeof link.target === 'object' ? link.target : store.nodeMap.get(link.target);
    const srcId = sourceNode?.id || link.source;
    const tgtId = targetNode?.id || link.target;

    // Non-blocking edges (parent-child, related, discovered-from) are rendered
    // subtly as dashed lines and never participate in blocking cascade/cycle colors.
    if (link.blocking === false) {
        if (store.connectedNodes.size > 0 && store.connectedNodes.has(srcId) && store.connectedNodes.has(tgtId)) {
            return THEME.link.gold + '99';
        }
        return (THEME.fgMuted || '#6272a4') + '88';
    }

    // What-if cascade links (bright green for unblocking edges)
    if (whatIfState.active && store.highlightedLinks.has(linkId)) {
        if (targetNode?._whatIfState === 'closing' || sourceNode?._whatIfState === 'unblocked') {
            return THEME.accent.green;
        }
    }

    // Critical path links (red)
    if (criticalPathState.active && store.highlightedLinks.has(linkId)) {
        if (sourceNode?._criticalPathState || targetNode?._criticalPathState) {
            return THEME.link.critical;
        }
    }

    // Cycle navigator recommended cut edge stands out in bright gold
    if (cycleNavigatorState.active && cycleNavigatorState.suggestedBreakEdgeKey === linkId) {
        return THEME.accent.gold;
    }

    // Highlighted links
    if (store.highlightedLinks.has(linkId)) return THEME.link.highlighted;

    // Gold glow for connected subgraph links
    if (store.connectedNodes.size > 0 && store.connectedNodes.has(srcId) && store.connectedNodes.has(tgtId)) {
        return THEME.link.gold + 'cc'; // Gold with alpha
    }

    // Cycle links
    if (sourceNode?.inCycle && targetNode?.inCycle) return THEME.link.cycle;

    // Cross-label edges in galaxy view (bv-qpt0)
    if (labelClusterState.active && isCrossLabelEdge(link)) {
        return THEME.accent.pink; // Distinct color for cross-label dependencies
    }

    // Tarjan bridge cut-edges (single-link bottlenecks on active subgraph)
    if (link.isBridge && !store.heatmapMode) {
        return (THEME.accent.orange || '#FFB86C') + 'cc';
    }

    // Heatmap mode: color links by topological hop slack of connected nodes (acyclic DAGs only)
    // Zero slack = critical path (red), higher hop float = relaxed (cyan)
    if (store.heatmapMode && !store.metrics.hasCycles && sourceNode && targetNode) {
        const srcSlack = sourceNode.slack;
        const tgtSlack = targetNode.slack;
        const srcHasSlack = srcSlack !== undefined && srcSlack !== null && Number.isFinite(srcSlack);
        const tgtHasSlack = tgtSlack !== undefined && tgtSlack !== null && Number.isFinite(tgtSlack);

        if (srcHasSlack || tgtHasSlack) {
            const minSlack = Math.min(
                srcHasSlack ? srcSlack : Infinity,
                tgtHasSlack ? tgtSlack : Infinity
            );

            // Normalize slack in dependency hops (0..maxHopScale hops, minimum scale of 5 hops)
            const maxHopScale = Math.max(store.maxMetrics.slack || 0, 5);
            const normalizedSlack = Math.min(minSlack, maxHopScale) / maxHopScale;

            // Color gradient: red (0 hops critical) -> orange -> yellow -> cyan (high hop float)
            if (normalizedSlack < 0.25) {
                return THEME.priority[0] + 'aa'; // Red with alpha (critical / 0-1 hop slack)
            } else if (normalizedSlack < 0.5) {
                return THEME.priority[1] + 'aa'; // Orange (low hop float)
            } else if (normalizedSlack < 0.75) {
                return THEME.accent.yellow + 'aa'; // Yellow (moderate hop float)
            } else {
                return THEME.accent.cyan + '88'; // Cyan (high hop float)
            }
        }
    }

    return THEME.link.default;
}

function getLinkOpacity(link) {
    if (store.highlightedLinks.size > 0) {
        const linkId = edgeKey(link.source?.id || link.source, link.target?.id || link.target);
        return store.highlightedLinks.has(linkId) ? 1 : 0.15;
    }
    if (store.highlightedNodes.size > 0) {
        const sourceId = link.source?.id || link.source;
        const targetId = link.target?.id || link.target;
        if (!store.highlightedNodes.has(sourceId) && !store.highlightedNodes.has(targetId)) {
            return 0.15;
        }
    }
    // Dim non-connected links when showing connected subgraph
    if (store.connectedNodes.size > 0) {
        const sourceId = link.source?.id || link.source;
        const targetId = link.target?.id || link.target;
        if (store.connectedNodes.has(sourceId) && store.connectedNodes.has(targetId)) {
            return 1; // Full opacity for connected links
        }
        return 0.1; // Very dim for non-connected
    }
    return 0.6;
}

/**
 * Get link width based on metric or highlight state.
 * Links connecting high-importance nodes (by PageRank, betweenness) are thicker.
 */
function getLinkWidth(link, globalScale) {
    const sourceNode = typeof link.source === 'object' ? link.source : store.nodeMap.get(link.source);
    const targetNode = typeof link.target === 'object' ? link.target : store.nodeMap.get(link.target);
    const srcId = sourceNode?.id || link.source;
    const tgtId = targetNode?.id || link.target;
    const linkId = edgeKey(srcId, tgtId);

    // Cycle navigator recommended break edge gets extra emphasis (blocking edges only)
    if (link.blocking !== false && cycleNavigatorState.active && cycleNavigatorState.suggestedBreakEdgeKey === linkId) {
        return Math.max(4, 4 / globalScale);
    }

    // Highlighted links (what-if, critical path, etc.) get thick width
    if (store.highlightedLinks.has(linkId)) {
        return Math.max(3, 3 / globalScale);
    }

    // Gold connected subgraph links get medium-thick width
    if (store.connectedNodes.size > 0 && store.connectedNodes.has(srcId) && store.connectedNodes.has(tgtId)) {
        return Math.max(2.5, 3 / globalScale);
    }

    // Tarjan bridge cut-edges get medium emphasis (respect heatmap & cycle precedence)
    if (link.isBridge && !store.heatmapMode && !(sourceNode?.inCycle && targetNode?.inCycle)) {
        return Math.max(2, 2.2 / globalScale);
    }

    // Critical path edges
    if (sourceNode?.criticalDepth > 0 && targetNode?.criticalDepth > 0) {
        return Math.max(2, 2 / globalScale);
    }

    // Metric-based width scaling when heatmap is active
    // Links connecting high-importance nodes get thicker edges
    if (store.heatmapMode && sourceNode && targetNode) {
        // Combine metrics: use average of source+target importance
        const srcPagerank = sourceNode.pagerank || 0;
        const tgtPagerank = targetNode.pagerank || 0;
        const srcBetweenness = sourceNode.betweenness || 0;
        const tgtBetweenness = targetNode.betweenness || 0;

        // Normalize by max values
        const avgPagerank = (srcPagerank + tgtPagerank) / 2 / (store.maxMetrics.pagerank || 1);
        const avgBetweenness = (srcBetweenness + tgtBetweenness) / 2 / (store.maxMetrics.betweenness || 1);

        // Combine metrics (70% PageRank, 30% betweenness for importance)
        const importance = avgPagerank * 0.7 + avgBetweenness * 0.3;

        // Scale width from 1 to 3 based on importance
        const baseWidth = 1 + importance * 2;
        return Math.max(baseWidth, baseWidth / globalScale);
    }

    // Default width
    return Math.max(1, 1.5 / globalScale);
}

function drawLink(link, ctx, globalScale) {
    const start = link.source;
    const end = link.target;

    // Check for undefined coordinates (not falsy - 0 is valid)
    if (start.x === undefined || end.x === undefined) return;

    const color = getLinkColor(link);
    const opacity = getLinkOpacity(link);
    const width = getLinkWidth(link, globalScale);

    const linkId = edgeKey(start.id || start, end.id || end);
    const isSuggestedCycleBreak = link.blocking !== false && cycleNavigatorState.active && cycleNavigatorState.suggestedBreakEdgeKey === linkId;
    ctx.save();
    ctx.globalAlpha = opacity;
    ctx.strokeStyle = color;
    ctx.lineWidth = width;
    if (isSuggestedCycleBreak) {
        ctx.setLineDash([6 / globalScale, 3 / globalScale]);
    } else if (link.blocking === false) {
        ctx.setLineDash([4 / globalScale, 4 / globalScale]);
    } else if (link.isBridge && !store.heatmapMode && !(start?.inCycle && end?.inCycle)) {
        ctx.setLineDash([6 / globalScale, 3 / globalScale]);
    }

    // Curved link
    const dx = end.x - start.x;
    const dy = end.y - start.y;
    const dist = Math.sqrt(dx * dx + dy * dy);
    const curvature = 0.2;
    const cx = (start.x + end.x) / 2 + dy * curvature;
    const cy = (start.y + end.y) / 2 - dx * curvature;

    ctx.beginPath();
    ctx.moveTo(start.x, start.y);
    ctx.quadraticCurveTo(cx, cy, end.x, end.y);
    ctx.stroke();
    ctx.setLineDash([]);

    // Arrowhead
    const endSize = getNodeSize(end);
    const arrowLen = Math.min(10, 8 / globalScale);

    // Skip arrow if nodes overlap (dist too small)
    if (dist < endSize + 1) {
        ctx.restore();
        return;
    }

    // Calculate arrow position (at edge of target node)
    const t = 1 - endSize / dist;
    const arrowX = start.x + t * dx;
    const arrowY = start.y + t * dy;

    const angle = Math.atan2(end.y - start.y, end.x - start.x);
    ctx.fillStyle = color;
    ctx.beginPath();
    ctx.moveTo(arrowX, arrowY);
    ctx.lineTo(
        arrowX - arrowLen * Math.cos(angle - Math.PI / 6),
        arrowY - arrowLen * Math.sin(angle - Math.PI / 6)
    );
    ctx.lineTo(
        arrowX - arrowLen * Math.cos(angle + Math.PI / 6),
        arrowY - arrowLen * Math.sin(angle + Math.PI / 6)
    );
    ctx.closePath();
    ctx.fill();

    ctx.restore();
}

// ============================================================================
// EVENT HANDLERS
// ============================================================================

function handleNodeClick(node, event) {
    if (!node) return;

    // W30-4 / P18B-2: While picking a target node for Check Add Dep, dispatch
    // nodeClick without opening the detail drawer or running modifier actions.
    if (store.checkDepPicking) {
        dispatchEvent('nodeClick', { node, event });
        return;
    }

    // Shift+click: what-if simulation
    // Ctrl+click: show dependencies
    // Regular click: select

    if (event?.shiftKey) {
        if (whatIfState.active && whatIfState.sourceNodes.length > 0) {
            const exists = whatIfState.sourceNodes.some(n => n.id === node.id);
            if (exists) {
                const remaining = whatIfState.sourceNodes.filter(n => n.id !== node.id);
                if (remaining.length === 0) {
                    resetWhatIf();
                } else if (remaining.length === 1) {
                    performWhatIf(remaining[0]);
                } else {
                    performBatchWhatIf(remaining);
                }
            } else {
                performBatchWhatIf([...whatIfState.sourceNodes, node]);
            }
        } else {
            performWhatIf(node);
        }
    } else if (event?.ctrlKey || event?.metaKey) {
        highlightDependencyPath(node);
    } else {
        if (whatIfState.active) {
            resetWhatIf();
        }
        if (criticalPathState.active) {
            resetCriticalPath();
        }
        if (cycleNavigatorState.active) {
            resetCycleNavigator();
        }
        selectNode(node);
    }

    dispatchEvent('nodeClick', { node, event });
}

function handleNodeRightClick(node, event) {
    event.preventDefault();
    dispatchEvent('nodeContextMenu', { node, event, x: event.clientX, y: event.clientY });
}

function handleNodeHover(node, prevNode) {
    store.hoveredNode = node;

    // Update connected nodes for gold glow effect
    updateConnectedNodes(node);

    // Update cursor
    if (store.container) {
        store.container.style.cursor = node ? 'pointer' : 'default';
    }

    // Show tooltip
    if (node) {
        showTooltip(node);
    } else {
        hideTooltip();
    }

    // Refresh graph to show gold glow
    refreshGraph();

    dispatchEvent('nodeHover', { node, prevNode });
}

function handleNodeDrag(node) {
    // Real-time position update
    dispatchEvent('nodeDrag', { node });
}

function handleNodeDragEnd(node) {
    // Fix position after drag
    node.fx = node.x;
    node.fy = node.y;
    dispatchEvent('nodeDragEnd', { node });
}

function handleLinkClick(link, event) {
    dispatchEvent('linkClick', { link, event });
}

function handleLinkHover(link, prevLink) {
    dispatchEvent('linkHover', { link, prevLink });
}

function handleBackgroundClick(event) {
    clearSelection();
    dispatchEvent('backgroundClick', { event });
}

function handleZoom(transform) {
    dispatchEvent('zoom', { transform, scale: transform.k });
}

// ============================================================================
// WHAT-IF SIMULATION
// ============================================================================

// What-if animation state
const whatIfState = {
    active: false,
    sourceNode: null,
    sourceNodes: [],
    unblockedNodes: new Set(),
    animationPhase: 0,
    animationTimer: null,
    unblockTimers: new Set()
};

function resolveGraphNodeObject(nodeOrId) {
    if (!nodeOrId) return null;
    if (typeof nodeOrId === 'string') {
        return store.graph?.graphData()?.nodes?.find(n => n.id === nodeOrId) || store.nodeMap.get(nodeOrId) || null;
    }
    return nodeOrId;
}

function clampGraphWhatIfResult(result, realCount) {
    if (!result || typeof result !== 'object') return result;
    const unblockedIds = (result.unblocked_ids || []).filter(i => i < realCount);
    const cascadeIds = (result.cascade_ids || []).filter(i => i < realCount);
    return {
        ...result,
        unblocked_ids: unblockedIds,
        cascade_ids: cascadeIds,
        direct_unblocks: unblockedIds.length,
        transitive_unblocks: cascadeIds.length,
    };
}

/**
 * Perform what-if simulation for closing an issue
 * @param {Object|string} nodeInput - The node (or node ID) to simulate closing
 */
export function performWhatIf(nodeInput) {
    const node = resolveGraphNodeObject(nodeInput);
    if (!node || !store.wasmReady || !store.wasmGraph) {
        console.warn('[bv-graph] What-if: WASM not ready');
        return null;
    }

    // Resolved issues no longer contribute blocking work.
    if (isResolvedIssue(node)) {
        showToast('Issue is already resolved', 'info');
        return null;
    }

    const idx = store.wasmGraph.nodeIdx(node.id);
    if (idx === undefined) return null;

    const whatIfGraph = store.whatIfWasmGraph || store.wasmGraph;
    const realCount = store.wasmGraph.nodeCount();

    // Call what-if (dynamic parent-stopping model when parent-child edges exist, else WASM)
    let result;
    try {
        if (store.whatIfModel?.hasParentChild) {
            result = simulateGraphWhatIfCloseIndices(store.whatIfModel, [idx]);
        } else {
            const closedSet = buildClosedSet();
            result = whatIfGraph.whatIfClose(idx, closedSet);
            if (typeof result === 'string') {
                result = JSON.parse(result);
            }
            result = clampGraphWhatIfResult(result, realCount);
        }
    } catch (e) {
        console.error('[bv-graph] What-if computation failed:', e);
        return null;
    }

    // Clear competing overlays and animate the cascade
    resetCycleNavigator();
    resetCriticalPath();
    resetDependencyCone({ refresh: false });
    animateWhatIfCascade([node], result);

    return result;
}

/**
 * Perform multi-node batch what-if simulation using WASM whatIfCloseBatch
 * @param {Array<Object|string>} nodesInput - Array of nodes or node IDs to simulate closing together
 */
export function performBatchWhatIf(nodesInput) {
    if (!Array.isArray(nodesInput) || nodesInput.length === 0 || !store.wasmReady || !store.wasmGraph) {
        return null;
    }

    const uniqueNodes = [];
    const seenIds = new Set();
    for (const item of nodesInput) {
        const node = resolveGraphNodeObject(item);
        if (!node || seenIds.has(node.id)) continue;
        if (isResolvedIssue(node)) {
            showToast(`Issue ${node.id} is already resolved`, 'info');
            continue;
        }
        seenIds.add(node.id);
        uniqueNodes.push(node);
    }

    if (uniqueNodes.length === 0) return null;
    if (uniqueNodes.length === 1) return performWhatIf(uniqueNodes[0]);

    const indices = uniqueNodes
        .map(n => store.wasmGraph.nodeIdx(n.id))
        .filter(idx => idx !== undefined);
    if (indices.length === 0) return null;

    const whatIfGraph = store.whatIfWasmGraph || store.wasmGraph;
    const realCount = store.wasmGraph.nodeCount();
    let result;
    try {
        if (store.whatIfModel?.hasParentChild) {
            result = simulateGraphWhatIfCloseIndices(store.whatIfModel, indices);
        } else {
            const closedSet = buildClosedSet();
            result = whatIfGraph.whatIfCloseBatch(new Uint32Array(indices), closedSet);
            if (typeof result === 'string') {
                result = JSON.parse(result);
            }
            result = clampGraphWhatIfResult(result, realCount);
        }
    } catch (e) {
        console.error('[bv-graph] Batch what-if computation failed:', e);
        return null;
    }

    resetCycleNavigator();
    resetCriticalPath();
    resetDependencyCone({ refresh: false });
    animateWhatIfCascade(uniqueNodes, result);
    return result;
}

/**
 * Build a boolean array indicating which nodes are already closed
 */
function buildClosedSet() {
    const whatIfGraph = store.whatIfWasmGraph || store.wasmGraph;
    const n = whatIfGraph.nodeCount();
    const realCount = store.wasmGraph.nodeCount();
    const closedSet = new Uint8Array(n);

    store.issues.forEach(issue => {
        if (isResolvedIssue(issue)) {
            const idx = store.wasmGraph.nodeIdx(issue.id);
            if (idx !== undefined && idx < realCount) {
                closedSet[idx] = 1;
            }
        }
    });

    for (const id of window.beadsViewer?.getResolvedIssueIDs?.() || []) {
        const idx = store.wasmGraph.nodeIdx(id);
        if (idx !== undefined && idx < realCount) closedSet[idx] = 1;
    }

    return closedSet;
}

/**
 * Animate the what-if cascade effect (supports single or multi-node batch close)
 */
function animateWhatIfCascade(sourceNodesInput, result) {
    const sourceNodes = Array.isArray(sourceNodesInput) ? sourceNodesInput : [sourceNodesInput];
    const primarySource = sourceNodes[0] || null;
    const sourceIdSet = new Set(sourceNodes.map(n => n.id));

    // Clear any existing animation
    resetWhatIf();

    whatIfState.active = true;
    whatIfState.sourceNode = primarySource;
    whatIfState.sourceNodes = sourceNodes;
    whatIfState.unblockedNodes.clear();

    const realCount = store.wasmGraph ? store.wasmGraph.nodeCount() : 0;

    // Get unblocked node IDs
    const unblockedIds = (result.unblocked_ids || [])
        .filter(idx => idx < realCount)
        .map(idx => store.wasmGraph.nodeId(idx))
        .filter(Boolean);

    // Phase 1: Highlight the source node(s) (pulse green, "closing")
    store.highlightedNodes.clear();
    store.highlightedLinks.clear();
    const graphData = store.graph.graphData();
    sourceNodes.forEach(src => {
        store.highlightedNodes.add(src.id);
        const sourceGraphNode = graphData.nodes.find(n => n.id === src.id);
        if (sourceGraphNode) {
            sourceGraphNode._whatIfState = 'closing';
        }
    });

    refreshGraph();
    dispatchEvent('whatIfStart', { node: primarySource, nodes: sourceNodes, result });

    // Phase 2: Ripple out to unblocked nodes with staggered animation
    whatIfState.animationPhase = 1;
    let delay = 300;
    const runToken = whatIfState.sourceNodes;

    unblockedIds.forEach((id, i) => {
        const timer = setTimeout(() => {
            whatIfState.unblockTimers.delete(timer);
            if (!whatIfState.active || whatIfState.sourceNodes !== runToken) return;
            whatIfState.unblockedNodes.add(id);
            store.highlightedNodes.add(id);

            // Mark node as unblocked on current graphData (handles mid-animation filter rebuilds)
            const currentGraphData = store.graph?.graphData();
            const unblockedNode = currentGraphData?.nodes?.find(n => n.id === id);
            if (unblockedNode) {
                unblockedNode._whatIfState = 'unblocked';
            }

            // Rendered edges point from the dependent to its prerequisite.
            store.dependencies.forEach(dep => {
                if (!isBlockingDependency(dep) || dep.issue_id !== id) return;
                const blocker = store.nodeMap.get(dep.depends_on_id);
                if (sourceIdSet.has(dep.depends_on_id) || (blocker && isResolvedIssue(blocker))) {
                    store.highlightedLinks.add(edgeKey(id, dep.depends_on_id));
                }
            });

            refreshGraph();
            dispatchEvent('whatIfUnblock', { nodeId: id, index: i, total: unblockedIds.length });
        }, delay + i * 150);
        whatIfState.unblockTimers.add(timer);
    });

    // Phase 3: Show summary after animations complete
    const summaryDelay = delay + unblockedIds.length * 150 + 200;
    whatIfState.animationTimer = setTimeout(() => {
        whatIfState.animationPhase = 2;
        showWhatIfSummary(sourceNodes, result, unblockedIds);
    }, summaryDelay);
}

/**
 * Show what-if summary popup
 */
function showWhatIfSummary(sourceNodesInput, result, unblockedIds) {
    const sourceNodes = Array.isArray(sourceNodesInput) ? sourceNodesInput : [sourceNodesInput];
    const sourceNode = sourceNodes[0] || null;
    const label = sourceNodes.length > 1
        ? `${sourceNodes.map(n => n.id).join(' + ')} (${sourceNodes.length} issues)`
        : (sourceNode?.id || 'issue');
    const directCount = result.direct_unblocks || unblockedIds.length;
    const transitiveCount = result.transitive_unblocks || directCount;
    const parallelGain = result.parallel_gain || 0;

    dispatchEvent('whatIfComplete', {
        node: sourceNode,
        nodes: sourceNodes,
        directUnblocks: directCount,
        transitiveUnblocks: transitiveCount,
        parallelGain: parallelGain,
        unblockedIds: unblockedIds
    });

    // Create summary toast
    if (directCount > 0) {
        showToast(
            `Closing ${label} would unblock ${directCount} issue${directCount > 1 ? 's' : ''} directly` +
            (transitiveCount > directCount ? `, ${transitiveCount} total in cascade` : ''),
            'success'
        );
    } else {
        showToast(`Closing ${label} would not immediately unblock any issues`, 'info');
    }
}

/**
 * Reset what-if visualization state
 */
export function resetWhatIf({ force = false } = {}) {
    for (const timer of whatIfState.unblockTimers) clearTimeout(timer);
    whatIfState.unblockTimers.clear();
    if (whatIfState.animationTimer) {
        clearTimeout(whatIfState.animationTimer);
        whatIfState.animationTimer = null;
    }

    const wasActive = whatIfState.active || whatIfState.sourceNodes.length > 0 || whatIfState.unblockedNodes.size > 0;
    if (!wasActive) {
        if (force) {
            dispatchEvent('whatIfReset');
        }
        return;
    }

    // Clear visual states
    const graphData = store.graph?.graphData();
    if (graphData) {
        graphData.nodes.forEach(node => {
            delete node._whatIfState;
        });
    }

    whatIfState.active = false;
    whatIfState.sourceNode = null;
    whatIfState.sourceNodes = [];
    whatIfState.unblockedNodes.clear();
    whatIfState.animationPhase = 0;

    store.highlightedNodes.clear();
    store.highlightedLinks.clear();
    refreshGraph();

    dispatchEvent('whatIfReset');
}

/**
 * Check if what-if simulation is active
 */
export function isWhatIfActive() {
    return whatIfState.active;
}

/**
 * Enable or disable interactive target-node picking mode for Check Add Dep (W30-4 / P18B-2).
 * @param {boolean} active
 */
export function setCheckDepPicking(active) {
    store.checkDepPicking = Boolean(active);
}

/**
 * Inspect current canvas highlight sets (string node IDs and edgeKey strings).
 */
export function getHighlightState() {
    return {
        nodes: [...store.highlightedNodes],
        links: [...store.highlightedLinks],
        checkDepPicking: Boolean(store.checkDepPicking)
    };
}

/**
 * Highlight a proposed-dependency path (or cycle path) on the force-graph canvas (W30-4 / P18B-1).
 * Clears competing visual overlays so the cycle/proposed edge stands out cleanly.
 * @param {string[]} nodeIds
 */
export function highlightCheckDepPath(nodeIds = []) {
    resetWhatIf({ force: true });
    resetCycleNavigator();
    resetCriticalPath();
    resetDependencyCone({ refresh: false });
    store.highlightedNodes.clear();
    store.highlightedLinks.clear();
    const graphData = store.graph?.graphData?.();
    if (!graphData || !Array.isArray(nodeIds) || nodeIds.length === 0) {
        refreshGraph();
        return;
    }
    const idSet = new Set(nodeIds);
    for (const n of graphData.nodes || []) {
        if (idSet.has(n.id)) {
            store.highlightedNodes.add(n.id);
        }
    }
    const pairSet = new Set();
    for (let i = 0; i + 1 < nodeIds.length; i++) {
        pairSet.add(edgeKey(nodeIds[i], nodeIds[i + 1]));
        pairSet.add(edgeKey(nodeIds[i + 1], nodeIds[i]));
    }
    for (const l of graphData.links || []) {
        if (l.blocking === false) continue;
        const s = typeof l.source === 'object' ? l.source.id : l.source;
        const t = typeof l.target === 'object' ? l.target.id : l.target;
        const k = edgeKey(s, t);
        if (pairSet.has(k)) {
            store.highlightedLinks.add(k);
        }
    }
    refreshGraph();
}

/**
 * Clear proposed-dependency path highlight from the force-graph canvas (W30-4).
 */
export function clearCheckDepHighlight() {
    store.checkDepPicking = false;
    store.highlightedNodes.clear();
    store.highlightedLinks.clear();
    refreshGraph();
}

/**
 * Get what-if state
 */
export function getWhatIfState() {
    const state = {
        active: whatIfState.active,
        sourceNode: whatIfState.sourceNode,
        unblockedCount: whatIfState.unblockedNodes.size
    };
    if (whatIfState.sourceNodes.length > 1) {
        state.sourceNodes = whatIfState.sourceNodes.map(n => n.id);
    }
    return state;
}

/**
 * Get batch what-if selected source node IDs
 */
export function getBatchWhatIfSourceIds() {
    return whatIfState.sourceNodes.map(n => n.id);
}

// ============================================================================
// CRITICAL PATH ANIMATION
// ============================================================================

// Critical path animation & [1/N] kCriticalPaths stepper state
const criticalPathState = {
    active: false,
    paths: [],          // Array of { ids: string[], indices: number[], length: number }
    currentIndex: 0,
    path: [],           // Node IDs in active path order
    pathIndices: [],    // Node indices in active path order
    currentStep: 0,
    animationTimer: null,
    pathLength: 0
};

function emitCriticalPathChange() {
    dispatchEvent('criticalPathChange', {
        active: criticalPathState.active,
        currentIndex: criticalPathState.currentIndex,
        pathCount: criticalPathState.paths.length,
        path: [...criticalPathState.path],
        length: criticalPathState.pathLength,
        pathLabel: criticalPathState.path.join(' → ')
    });
}

function computeTopCriticalPaths(k = 5) {
    const activeGraph = store.activeWasmGraph || store.wasmGraph;
    if (!store.wasmReady || !activeGraph || activeGraph.hasCycles()) {
        return [];
    }
    let pathResult;
    try {
        pathResult = typeof activeGraph.kCriticalPaths === 'function'
            ? activeGraph.kCriticalPaths(k)
            : activeGraph.kCriticalPathsDefault();
        if (typeof pathResult === 'string') {
            pathResult = JSON.parse(pathResult);
        }
    } catch (e) {
        console.error('[bv-graph] Critical path computation failed:', e);
        return [];
    }
    if (!Array.isArray(pathResult?.paths)) return [];
    return pathResult.paths
        .map(entry => {
            const indices = Array.isArray(entry?.nodes) ? entry.nodes : [];
            const ids = indices.map(idx => activeGraph.nodeId(idx)).filter(Boolean);
            return {
                ids,
                indices,
                length: entry?.length ?? ids.length
            };
        })
        .filter(entry => entry.ids.length > 0);
}

/**
 * Highlight a specific critical path [0..N-1] from kCriticalPaths(5)
 * @param {number} index - 0-based path index
 * @param {boolean} animate - Whether to animate traversal
 */
export function highlightCriticalPathByIndex(index = 0, animate = false) {
    const activeGraph = store.activeWasmGraph || store.wasmGraph;
    if (!store.wasmReady || !activeGraph) {
        console.warn('[bv-graph] Critical path: WASM not ready');
        return null;
    }

    if (!criticalPathState.paths.length) {
        const computed = computeTopCriticalPaths(5);
        if (!computed.length) {
            showToast('No critical path found (graph may be empty or cyclic)', 'info');
            return null;
        }
        criticalPathState.paths = computed;
    }

    if (criticalPathState.animationTimer) {
        clearTimeout(criticalPathState.animationTimer);
        criticalPathState.animationTimer = null;
    }
    const graphData = store.graph?.graphData();
    if (graphData) {
        graphData.nodes.forEach(node => {
            delete node._criticalPathState;
        });
    }

    const total = criticalPathState.paths.length;
    const normalizedIdx = ((index % total) + total) % total;
    const chosen = criticalPathState.paths[normalizedIdx];

    criticalPathState.active = true;
    criticalPathState.currentIndex = normalizedIdx;
    criticalPathState.path = [...chosen.ids];
    criticalPathState.pathIndices = [...chosen.indices];
    criticalPathState.pathLength = chosen.length;
    criticalPathState.currentStep = animate ? 0 : chosen.ids.length;

    store.highlightedNodes.clear();
    store.highlightedLinks.clear();

    emitCriticalPathChange();
    dispatchEvent('criticalPathStart', {
        currentIndex: normalizedIdx,
        pathCount: total,
        pathLength: criticalPathState.pathLength,
        nodeCount: criticalPathState.path.length
    });

    if (animate) {
        animateCriticalPathTraversal();
    } else {
        criticalPathState.path.forEach(id => {
            store.highlightedNodes.add(id);
            const gNode = graphData?.nodes?.find(n => n.id === id);
            if (gNode) gNode._criticalPathState = 'active';
        });
        highlightCriticalPathLinks();
        refreshGraph();
        showCriticalPathSummary();
    }

    return {
        currentIndex: normalizedIdx,
        pathCount: total,
        path: [...criticalPathState.path],
        length: criticalPathState.pathLength
    };
}

/**
 * Animate the critical path traversal
 * @param {boolean} animate - Whether to animate or just highlight
 */
export function animateCriticalPath(animate = true) {
    const activeGraph = store.activeWasmGraph || store.wasmGraph;
    if (!store.wasmReady || !activeGraph) {
        console.warn('[bv-graph] Critical path: WASM not ready');
        return null;
    }

    const paths = computeTopCriticalPaths(5);
    if (!paths.length) {
        showToast('No critical path found (graph may be empty or cyclic)', 'info');
        return null;
    }

    resetWhatIf();
    resetCycleNavigator();
    resetDependencyCone({ refresh: false });
    resetCriticalPath();
    criticalPathState.paths = paths;

    return highlightCriticalPathByIndex(0, animate);
}

/**
 * Step to next critical path [1/N]
 */
export function nextCriticalPath() {
    if (!criticalPathState.active || criticalPathState.paths.length === 0) {
        return animateCriticalPath(false);
    }
    return highlightCriticalPathByIndex(criticalPathState.currentIndex + 1, false);
}

/**
 * Step to previous critical path [1/N]
 */
export function prevCriticalPath() {
    if (!criticalPathState.active || criticalPathState.paths.length === 0) {
        return animateCriticalPath(false);
    }
    return highlightCriticalPathByIndex(criticalPathState.currentIndex - 1, false);
}

/**
 * Zoom to fit nodes on the currently active critical path
 */
export function zoomToCriticalPath() {
    if (!criticalPathState.active || !criticalPathState.path.length || !store.graph) return;
    const idSet = new Set(criticalPathState.path);
    store.graph.zoomToFit(400, 60, node => idSet.has(node.id));
}

/**
 * Animate the critical path traversal step by step
 */
function animateCriticalPathTraversal() {
    const path = criticalPathState.path;
    const graphData = store.graph.graphData();

    criticalPathState.currentStep = 0;

    function animateStep() {
        if (!criticalPathState.active || criticalPathState.currentStep >= path.length) {
            // Animation complete
            criticalPathState.animationTimer = setTimeout(() => {
                showCriticalPathSummary();
            }, 300);
            return;
        }

        const nodeId = path[criticalPathState.currentStep];
        store.highlightedNodes.add(nodeId);

        // Mark node with critical path state for visual effect
        const currentGraphData = store.graph?.graphData();
        const graphNode = currentGraphData?.nodes?.find(n => n.id === nodeId);
        if (graphNode) {
            graphNode._criticalPathState = 'active';
        }

        // Highlight the edge from previous node
        if (criticalPathState.currentStep > 0) {
            const prevNodeId = path[criticalPathState.currentStep - 1];
            store.highlightedLinks.add(edgeKey(prevNodeId, nodeId));
            store.highlightedLinks.add(edgeKey(nodeId, prevNodeId)); // Both directions
        }

        refreshGraph();
        dispatchEvent('criticalPathStep', {
            nodeId,
            step: criticalPathState.currentStep,
            total: path.length
        });

        criticalPathState.currentStep++;
        criticalPathState.animationTimer = setTimeout(animateStep, 250);
    }

    animateStep();
}

/**
 * Highlight all links on the critical path
 */
function highlightCriticalPathLinks() {
    const path = criticalPathState.path;
    for (let i = 0; i < path.length - 1; i++) {
        store.highlightedLinks.add(edgeKey(path[i], path[i+1]));
        store.highlightedLinks.add(edgeKey(path[i+1], path[i]));
    }
}

/**
 * Show critical path summary toast
 */
function showCriticalPathSummary() {
    const path = criticalPathState.path;
    if (path.length === 0) return;

    const sourceId = path[0];
    const sinkId = path[path.length - 1];
    const rankPrefix = criticalPathState.paths.length > 1
        ? `[${criticalPathState.currentIndex + 1}/${criticalPathState.paths.length}] `
        : '';

    showToast(
        `${rankPrefix}Critical path: ${path.length} nodes, from ${sourceId} to ${sinkId}`,
        'info'
    );

    dispatchEvent('criticalPathComplete', {
        currentIndex: criticalPathState.currentIndex,
        pathCount: criticalPathState.paths.length,
        path: criticalPathState.path,
        length: criticalPathState.pathLength,
        source: sourceId,
        sink: sinkId
    });
}

/**
 * Reset critical path visualization state
 */
export function resetCriticalPath() {
    if (criticalPathState.animationTimer) {
        clearTimeout(criticalPathState.animationTimer);
        criticalPathState.animationTimer = null;
    }

    const wasActive = criticalPathState.active || criticalPathState.path.length > 0 || criticalPathState.paths.length > 0;
    if (!wasActive) return;

    // Clear visual states
    const graphData = store.graph?.graphData();
    if (graphData) {
        graphData.nodes.forEach(node => {
            delete node._criticalPathState;
        });
    }

    criticalPathState.active = false;
    criticalPathState.paths = [];
    criticalPathState.currentIndex = 0;
    criticalPathState.path = [];
    criticalPathState.pathIndices = [];
    criticalPathState.currentStep = 0;
    criticalPathState.pathLength = 0;

    store.highlightedNodes.clear();
    store.highlightedLinks.clear();
    refreshGraph();

    dispatchEvent('criticalPathReset');
}

/**
 * Toggle critical path highlighting
 */
export function toggleCriticalPath() {
    if (criticalPathState.active) {
        resetCriticalPath();
    } else {
        animateCriticalPath(true);
    }
}

/**
 * Check if critical path is active
 */
export function isCriticalPathActive() {
    return criticalPathState.active;
}

/**
 * Get critical path state
 */
export function getCriticalPathState() {
    return {
        active: criticalPathState.active,
        currentIndex: criticalPathState.currentIndex,
        pathCount: criticalPathState.paths.length,
        paths: criticalPathState.paths.map(p => ({ path: [...p.ids], length: p.length })),
        path: [...criticalPathState.path],
        length: criticalPathState.pathLength,
        currentStep: criticalPathState.currentStep
    };
}

// ============================================================================
// SELECTION & HIGHLIGHTING
// ============================================================================

export function selectNode(node) {
    if (typeof node === 'string') {
        node = store.graph?.graphData()?.nodes?.find(n => n.id === node) || null;
    }
    if (whatIfState.active) {
        resetWhatIf({ force: true });
    }
    if (criticalPathState.active) {
        resetCriticalPath();
    }
    if (cycleNavigatorState.active) {
        resetCycleNavigator();
    }
    resetDependencyCone({ refresh: false });
    store.selectedNode = node;
    store.highlightedNodes.clear();
    store.highlightedLinks.clear();

    if (node) {
        store.highlightedNodes.add(node.id);
    }

    refreshGraph();
    dispatchEvent('selectionChange', { node });
}

export function clearSelection() {
    resetDependencyCone({ refresh: false });
    store.selectedNode = null;
    store.highlightedNodes.clear();
    store.highlightedLinks.clear();
    store.focusedPath = null;
    refreshGraph();
    dispatchEvent('selectionChange', { node: null });
}

export function highlightNodes(nodeIds) {
    store.highlightedNodes = new Set(nodeIds);
    refreshGraph();
}

export function highlightDependencyPath(node) {
    store.highlightedNodes.clear();
    store.highlightedLinks.clear();

    if (!node || !store.graph) return;

    let blockers = [];
    let dependents = [];

    const hasWasmReachability = store.wasmReady && store.wasmGraph &&
        typeof store.wasmGraph.reachableFrom === 'function' &&
        typeof store.wasmGraph.reachableTo === 'function';

    if (hasWasmReachability) {
        const idx = store.wasmGraph.nodeIdx(node.id);
        if (idx === undefined) return;
        // Get all nodes that block this one (upstream) and all blocked by it (downstream),
        // normalized to string IDs excluding self to match the JS BFS fallback.
        const rawBlockers = Array.from(store.wasmGraph.reachableFrom(idx) || []);
        const rawDependents = Array.from(store.wasmGraph.reachableTo(idx) || []);
        blockers = rawBlockers
            .map(i => store.wasmGraph.nodeId(i))
            .filter(id => id && id !== node.id);
        dependents = rawDependents
            .map(i => store.wasmGraph.nodeId(i))
            .filter(id => id && id !== node.id);
    } else {
        // Fallback BFS over blocking dependencies when reachability WASM feature is omitted.
        const blockingDeps = (store.dependencies || []).filter(isBlockingDependency);
        const blockerSet = new Set();
        const fwd = [node.id];
        while (fwd.length > 0) {
            const cur = fwd.shift();
            for (const d of blockingDeps) {
                if (d.issue_id === cur && d.depends_on_id && d.depends_on_id !== node.id && !blockerSet.has(d.depends_on_id)) {
                    blockerSet.add(d.depends_on_id);
                    fwd.push(d.depends_on_id);
                }
            }
        }
        const depSet = new Set();
        const rev = [node.id];
        while (rev.length > 0) {
            const cur = rev.shift();
            for (const d of blockingDeps) {
                if (d.depends_on_id === cur && d.issue_id && d.issue_id !== node.id && !depSet.has(d.issue_id)) {
                    depSet.add(d.issue_id);
                    rev.push(d.issue_id);
                }
            }
        }
        blockers = Array.from(blockerSet);
        dependents = Array.from(depSet);
    }

    store.highlightedNodes.add(node.id);
    for (const id of blockers) store.highlightedNodes.add(id);
    for (const id of dependents) store.highlightedNodes.add(id);

    // Highlight links
    const graphData = store.graph.graphData();
    graphData.links.forEach(link => {
        const sourceId = link.source?.id || link.source;
        const targetId = link.target?.id || link.target;
        if (store.highlightedNodes.has(sourceId) && store.highlightedNodes.has(targetId)) {
            store.highlightedLinks.add(edgeKey(sourceId, targetId));
        }
    });

    store.focusedPath = { center: node.id, blockers, dependents };
    refreshGraph();

    dispatchEvent('pathHighlight', { node, blockerCount: blockers.length, dependentCount: dependents.length });
}

export function highlightCriticalPath() {
    const activeGraph = store.activeWasmGraph || store.wasmGraph;
    if (!store.wasmReady || !activeGraph || activeGraph.hasCycles()) return;

    const criticalNodes = activeGraph.criticalPathNodes();
    store.highlightedNodes = new Set(
        criticalNodes.map(idx => activeGraph.nodeId(idx)).filter(Boolean)
    );
    refreshGraph();

    dispatchEvent('criticalPathHighlight', { nodeCount: criticalNodes.length });
}

export function highlightCycles() {
    if (!store.metrics.cycles?.cycles?.length) return;

    if (whatIfState.active) resetWhatIf({ force: true });
    if (criticalPathState.active) resetCriticalPath();
    resetDependencyCone({ refresh: false });
    resetCycleNavigator();
    store.highlightedLinks.clear();

    const activeGraph = store.activeWasmGraph || store.wasmGraph;
    const cycleNodeIndices = new Set(store.metrics.cycles.cycles.flat());
    store.highlightedNodes = new Set(
        [...cycleNodeIndices].map(idx => resolveCycleNodeId(idx, activeGraph)).filter(Boolean)
    );
    refreshGraph();

    dispatchEvent('cycleHighlight', { cycleCount: store.metrics.cycles.count });
}

export function getCycleInfo() {
    const activeGraph = store.activeWasmGraph || store.wasmGraph;
    const rawCycles = store.metrics.cycles?.cycles || [];
    const cycles = rawCycles
        .map(cycle => cycle.map(idx => resolveCycleNodeId(idx, activeGraph)).filter(Boolean))
        .filter(cycle => cycle.length > 0);
    const hasCycles = Boolean(store.metrics.hasCycles || cycles.length > 0);
    const cycleCount = hasCycles ? Math.max(Number(store.metrics.cycles?.count) || 0, cycles.length) : 0;
    return {
        hasCycles,
        cycleCount,
        truncated: Boolean(store.metrics.cycles?.truncated),
        cycles
    };
}

// ============================================================================
// CYCLE NAVIGATOR
// ============================================================================

// Cycle navigator state
const cycleNavigatorState = {
    active: false,
    cycles: [],           // Array of cycles (each cycle is array of node IDs)
    currentIndex: 0,
    highlightedCycleNodes: new Set(),
    highlightedCycleEdges: new Set(),
    suggestedBreakEdgeKey: null,
    suggestedBreakEdge: null
};

/**
 * Initialize cycle navigator with detected cycles
 */
export function initCycleNavigator() {
    if (!store.metrics.cycles?.cycles?.length) {
        cycleNavigatorState.active = false;
        cycleNavigatorState.cycles = [];
        cycleNavigatorState.currentIndex = 0;
        cycleNavigatorState.highlightedCycleNodes.clear();
        cycleNavigatorState.highlightedCycleEdges.clear();
        cycleNavigatorState.suggestedBreakEdgeKey = null;
        cycleNavigatorState.suggestedBreakEdge = null;
        dispatchEvent('cycleNavigatorInit', { hasCycles: false, cycleCount: 0 });
        return null;
    }

    const activeGraph = store.activeWasmGraph || store.wasmGraph;
    // Convert cycle indices to node IDs (works in both WASM and fallback modes)
    cycleNavigatorState.cycles = store.metrics.cycles.cycles.map(cycle =>
        cycle.map(idx => resolveCycleNodeId(idx, activeGraph)).filter(Boolean)
    ).filter(cycle => cycle.length > 0);

    cycleNavigatorState.currentIndex = 0;
    cycleNavigatorState.active = cycleNavigatorState.cycles.length > 0;

    dispatchEvent('cycleNavigatorInit', {
        hasCycles: cycleNavigatorState.active,
        cycleCount: cycleNavigatorState.cycles.length
    });

    return {
        cycleCount: cycleNavigatorState.cycles.length,
        cycles: cycleNavigatorState.cycles
    };
}

/**
 * Highlight a specific cycle by index
 * @param {number} index - Zero-based cycle index
 * @param {boolean} zoom - Whether to zoom to fit the cycle
 */
export function highlightCycle(index, zoom = false) {
    if (!cycleNavigatorState.active || cycleNavigatorState.cycles.length === 0) {
        return null;
    }

    // Clamp index to valid range
    index = Math.max(0, Math.min(index, cycleNavigatorState.cycles.length - 1));
    cycleNavigatorState.currentIndex = index;

    const cycle = cycleNavigatorState.cycles[index];
    if (!cycle || cycle.length === 0) return null;

    // Clear previous highlights
    store.highlightedNodes.clear();
    store.highlightedLinks.clear();

    // Highlight cycle nodes
    cycleNavigatorState.highlightedCycleNodes.clear();
    cycle.forEach(nodeId => {
        store.highlightedNodes.add(nodeId);
        cycleNavigatorState.highlightedCycleNodes.add(nodeId);
    });

    // Highlight only real active blocking cycle edges (including wraparound edge)
    const activeBlockingEdges = buildActiveBlockingEdgeKeys();
    cycleNavigatorState.highlightedCycleEdges.clear();
    for (let i = 0; i < cycle.length; i++) {
        const from = cycle[i];
        const to = cycle[(i + 1) % cycle.length];
        const edgeId = edgeKey(from, to);
        if (activeBlockingEdges.has(edgeId)) {
            store.highlightedLinks.add(edgeId);
            cycleNavigatorState.highlightedCycleEdges.add(edgeId);
        }
    }

    // Match top cycle-break recommendation to a visible edge in this highlighted cycle
    const graphData = store.graph?.graphData();
    const visibleIds = graphData ? new Set(graphData.nodes.map(n => n.id)) : null;
    cycleNavigatorState.suggestedBreakEdgeKey = null;
    cycleNavigatorState.suggestedBreakEdge = null;
    for (const suggestion of (store.metrics.cycleBreakSuggestions || [])) {
        if (cycleNavigatorState.highlightedCycleEdges.has(suggestion.edgeKey) &&
            (!visibleIds || (visibleIds.has(suggestion.from) && visibleIds.has(suggestion.to)))) {
            cycleNavigatorState.suggestedBreakEdgeKey = suggestion.edgeKey;
            cycleNavigatorState.suggestedBreakEdge = suggestion;
            break;
        }
    }

    // Mark nodes with cycle state for enhanced visual
    if (graphData) {
        graphData.nodes.forEach(node => {
            node._cycleHighlight = cycleNavigatorState.highlightedCycleNodes.has(node.id);
        });
    }

    refreshGraph();

    // Zoom to cycle if requested
    if (zoom) {
        zoomToCycle(index);
    }

    dispatchEvent('cycleHighlightChange', {
        currentIndex: index,
        cycleCount: cycleNavigatorState.cycles.length,
        cycle: cycle,
        cyclePath: formatCyclePath(cycle),
        suggestedBreakEdge: cycleNavigatorState.suggestedBreakEdge
    });

    return {
        index,
        cycle,
        path: formatCyclePath(cycle),
        suggestedBreakEdge: cycleNavigatorState.suggestedBreakEdge
    };
}

/**
 * Navigate to the next cycle
 */
export function nextCycle() {
    if (!cycleNavigatorState.active) return null;
    const nextIndex = (cycleNavigatorState.currentIndex + 1) % cycleNavigatorState.cycles.length;
    return highlightCycle(nextIndex, true);
}

/**
 * Navigate to the previous cycle
 */
export function prevCycle() {
    if (!cycleNavigatorState.active) return null;
    const prevIndex = (cycleNavigatorState.currentIndex - 1 + cycleNavigatorState.cycles.length) % cycleNavigatorState.cycles.length;
    return highlightCycle(prevIndex, true);
}

/**
 * Zoom to fit a specific cycle
 * @param {number} index - Zero-based cycle index
 */
export function zoomToCycle(index = cycleNavigatorState.currentIndex) {
    if (!cycleNavigatorState.active || !store.graph) return;

    const cycle = cycleNavigatorState.cycles[index];
    if (!cycle || cycle.length === 0) return;

    // Get node coordinates
    const graphData = store.graph.graphData();
    const cycleNodes = graphData.nodes.filter(n => cycle.includes(n.id));

    if (cycleNodes.length === 0) return;

    // Compute bounds
    const xs = cycleNodes.map(n => n.x);
    const ys = cycleNodes.map(n => n.y);
    const minX = Math.min(...xs);
    const maxX = Math.max(...xs);
    const minY = Math.min(...ys);
    const maxY = Math.max(...ys);

    // Center on cycle with some padding
    const centerX = (minX + maxX) / 2;
    const centerY = (minY + maxY) / 2;
    const width = maxX - minX + 100;
    const height = maxY - minY + 100;

    // Calculate zoom level to fit cycle
    const containerWidth = store.container?.clientWidth || 800;
    const containerHeight = store.container?.clientHeight || 600;
    const scaleX = containerWidth / width;
    const scaleY = containerHeight / height;
    const scale = Math.min(scaleX, scaleY, 2.5); // Cap at 2.5x zoom

    store.graph.centerAt(centerX, centerY, 400);
    store.graph.zoom(scale, 400);

    dispatchEvent('cycleZoom', { index, cycle, center: { x: centerX, y: centerY }, scale });
}

/**
 * Reset cycle navigator state
 */
export function resetCycleNavigator() {
    const wasActive = cycleNavigatorState.active ||
        cycleNavigatorState.highlightedCycleNodes.size > 0 ||
        cycleNavigatorState.highlightedCycleEdges.size > 0;
    if (!wasActive) return;

    cycleNavigatorState.active = false;
    cycleNavigatorState.cycles = [];
    cycleNavigatorState.currentIndex = 0;
    cycleNavigatorState.highlightedCycleNodes.clear();
    cycleNavigatorState.highlightedCycleEdges.clear();
    cycleNavigatorState.suggestedBreakEdgeKey = null;
    cycleNavigatorState.suggestedBreakEdge = null;

    // Clear cycle highlight state from nodes
    const graphData = store.graph?.graphData();
    if (graphData) {
        graphData.nodes.forEach(node => {
            delete node._cycleHighlight;
        });
    }

    store.highlightedNodes.clear();
    store.highlightedLinks.clear();
    refreshGraph();

    dispatchEvent('cycleNavigatorReset');
}

/**
 * Toggle cycle navigator (highlight all cycles or reset)
 */
export function toggleCycleNavigator() {
    if (cycleNavigatorState.active && cycleNavigatorState.highlightedCycleNodes.size > 0) {
        resetCycleNavigator();
    } else {
        const activeGraph = store.activeWasmGraph || store.wasmGraph;
        const rawCycles = store.metrics.cycles?.cycles || [];
        const resolvedCycles = rawCycles
            .map(cycle => cycle.map(idx => resolveCycleNodeId(idx, activeGraph)).filter(Boolean))
            .filter(cycle => cycle.length > 0);
        if (resolvedCycles.length === 0) {
            showToast('No cycles detected in the graph', 'info');
            return;
        }
        resetWhatIf();
        resetCriticalPath();
        resetDependencyCone({ refresh: false });
        initCycleNavigator();
        if (cycleNavigatorState.cycles.length > 0) {
            highlightCycle(0, true);
        }
    }
}

/**
 * Get current cycle navigator state
 */
export function getCycleNavigatorState() {
    return {
        active: cycleNavigatorState.active,
        cycleCount: cycleNavigatorState.cycles.length,
        currentIndex: cycleNavigatorState.currentIndex,
        currentCycle: cycleNavigatorState.cycles[cycleNavigatorState.currentIndex] || [],
        currentPath: formatCyclePath(cycleNavigatorState.cycles[cycleNavigatorState.currentIndex] || [])
    };
}

/**
 * Format a cycle as a readable path string
 */
function formatCyclePath(cycle) {
    if (!cycle || cycle.length === 0) return '';
    return cycle.join(' → ') + ' → ' + cycle[0];
}

// ============================================================================
// DEPENDENCY CONE ISOLATION (WASM dependencyCone)
// ============================================================================

const dependencyConeState = {
    active: false,
    focusNodeId: null,
    nodeIds: new Set()
};

function computeDependencyConeIds(focusId) {
    if (!focusId) return new Set();
    if (store.wasmReady && store.wasmGraph && typeof store.wasmGraph.dependencyCone === 'function') {
        const idx = store.wasmGraph.nodeIdx(focusId);
        if (idx !== undefined) {
            const coneIndices = store.wasmGraph.dependencyCone(idx);
            if (coneIndices && typeof coneIndices.length === 'number') {
                const ids = new Set();
                for (const rawIdx of Array.from(coneIndices)) {
                    const nid = store.wasmGraph.nodeId(rawIdx);
                    if (nid) ids.add(nid);
                }
                if (ids.size > 0) return ids;
            }
        }
    }

    // Fallback BFS over blocking dependencies (ancestors + self + descendants)
    const ids = new Set([focusId]);
    const blockingDeps = (store.dependencies || []).filter(isBlockingDependency);
    const fwd = [focusId];
    while (fwd.length > 0) {
        const cur = fwd.shift();
        for (const d of blockingDeps) {
            if (d.issue_id === cur && !ids.has(d.depends_on_id)) {
                ids.add(d.depends_on_id);
                fwd.push(d.depends_on_id);
            }
        }
    }
    const rev = [focusId];
    const visitedRev = new Set([focusId]);
    while (rev.length > 0) {
        const cur = rev.shift();
        for (const d of blockingDeps) {
            if (d.depends_on_id === cur && !visitedRev.has(d.issue_id)) {
                visitedRev.add(d.issue_id);
                ids.add(d.issue_id);
                rev.push(d.issue_id);
            }
        }
    }
    return ids;
}

function applyDependencyConeToGraphData(graphData) {
    if (!graphData || !dependencyConeState.active) return;
    store.highlightedNodes.clear();
    store.highlightedLinks.clear();
    for (const node of (graphData.nodes || [])) {
        const inCone = dependencyConeState.nodeIds.has(node.id);
        node._coneFocus = node.id === dependencyConeState.focusNodeId;
        node._coneMember = inCone;
        if (inCone) {
            store.highlightedNodes.add(node.id);
        }
    }
    for (const link of (graphData.links || [])) {
        if (link.blocking === false) continue;
        const srcId = link.source?.id || link.source;
        const tgtId = link.target?.id || link.target;
        if (dependencyConeState.nodeIds.has(srcId) && dependencyConeState.nodeIds.has(tgtId)) {
            store.highlightedLinks.add(edgeKey(srcId, tgtId));
        }
    }
}

export function focusDependencyCone(nodeOrId = null, center = true) {
    const focusId = typeof nodeOrId === 'string'
        ? nodeOrId
        : (nodeOrId?.id || store.selectedNode?.id || store.hoveredNode?.id || null);
    if (!focusId || !store.graph) return null;

    resetWhatIf();
    resetCriticalPath();
    resetCycleNavigator();

    const coneIds = computeDependencyConeIds(focusId);
    if (coneIds.size === 0) return null;

    dependencyConeState.active = true;
    dependencyConeState.focusNodeId = focusId;
    dependencyConeState.nodeIds = coneIds;

    const graphData = store.graph.graphData();
    applyDependencyConeToGraphData(graphData);

    const focusNode = graphData.nodes.find(n => n.id === focusId);
    if (center && focusNode && typeof focusNode.x === 'number' && typeof focusNode.y === 'number') {
        store.graph.centerAt(focusNode.x, focusNode.y, 400);
    }
    refreshGraph();

    const state = getDependencyConeState();
    dispatchEvent('dependencyConeChange', state);
    return state;
}

export function resetDependencyCone({ refresh = true } = {}) {
    const wasActive = dependencyConeState.active;
    dependencyConeState.active = false;
    dependencyConeState.focusNodeId = null;
    dependencyConeState.nodeIds = new Set();

    if (store.graph) {
        const graphData = store.graph.graphData();
        for (const node of (graphData?.nodes || [])) {
            delete node._coneFocus;
            delete node._coneMember;
        }
    }
    if (wasActive) {
        store.highlightedNodes.clear();
        store.highlightedLinks.clear();
        if (refresh) {
            refreshGraph();
        }
        dispatchEvent('dependencyConeReset', { active: false, focusNodeId: null, count: 0, nodeIds: [] });
    }
}

export function toggleDependencyCone(nodeOrId = null) {
    const targetId = typeof nodeOrId === 'string'
        ? nodeOrId
        : (nodeOrId?.id || store.selectedNode?.id || store.hoveredNode?.id || null);
    if (dependencyConeState.active && (!targetId || dependencyConeState.focusNodeId === targetId)) {
        resetDependencyCone();
        return false;
    }
    return Boolean(focusDependencyCone(targetId));
}

export function getDependencyConeState() {
    return {
        active: dependencyConeState.active,
        focusNodeId: dependencyConeState.focusNodeId,
        count: dependencyConeState.nodeIds.size,
        nodeIds: [...dependencyConeState.nodeIds]
    };
}

// ============================================================================
// FILTERING
// ============================================================================

function syncOverlaysAfterGraphRebuild(graphData) {
    if (!graphData || !Array.isArray(graphData.nodes)) return;
    computeMaxMetrics(graphData.nodes);
    const nodeById = new Map(graphData.nodes.map(n => [n.id, n]));

    // Rebind or clear selectedNode so filtering out the selected node never
    // leaves a stale drawer open or dims the entire visible graph to 30%,
    // while preserving active What-If / Critical Path / Cycle overlay highlight sets.
    if (store.selectedNode) {
        const rebound = nodeById.get(store.selectedNode.id);
        if (rebound) {
            store.selectedNode = rebound;
        } else if (cycleNavigatorState.active || criticalPathState.active || whatIfState.active || dependencyConeState.active) {
            store.selectedNode = null;
            store.focusedPath = null;
            dispatchEvent('selectionChange', { node: null });
        } else {
            clearSelection();
        }
    }

    // Clear hoveredNode + tooltip if the hovered node was filtered out.
    if (store.hoveredNode && !nodeById.has(store.hoveredNode.id)) {
        store.hoveredNode = null;
        store.connectedNodes.clear();
        hideTooltip();
    }

    if (cycleNavigatorState.active) {
        const hasVisibleCycleNode = [...cycleNavigatorState.highlightedCycleNodes]
            .some(id => nodeById.has(id));
        if (!hasVisibleCycleNode && cycleNavigatorState.highlightedCycleNodes.size > 0) {
            resetCycleNavigator();
        } else {
            graphData.nodes.forEach(node => {
                node._cycleHighlight = cycleNavigatorState.highlightedCycleNodes.has(node.id);
            });
            const prevBreakKey = cycleNavigatorState.suggestedBreakEdgeKey;
            let nextBreak = null;
            for (const suggestion of (store.metrics.cycleBreakSuggestions || [])) {
                if (cycleNavigatorState.highlightedCycleEdges.has(suggestion.edgeKey) &&
                    nodeById.has(suggestion.from) && nodeById.has(suggestion.to)) {
                    nextBreak = suggestion;
                    break;
                }
            }
            cycleNavigatorState.suggestedBreakEdge = nextBreak;
            cycleNavigatorState.suggestedBreakEdgeKey = nextBreak ? nextBreak.edgeKey : null;
            if (prevBreakKey !== cycleNavigatorState.suggestedBreakEdgeKey) {
                const cycle = cycleNavigatorState.cycles[cycleNavigatorState.currentIndex] || [];
                dispatchEvent('cycleHighlightChange', {
                    currentIndex: cycleNavigatorState.currentIndex,
                    cycleCount: cycleNavigatorState.cycles.length,
                    cycle,
                    cyclePath: formatCyclePath(cycle),
                    suggestedBreakEdge: cycleNavigatorState.suggestedBreakEdge
                });
            }
        }
    }

    if (criticalPathState.active) {
        const hasVisiblePathNode = criticalPathState.path.some(id => nodeById.has(id));
        if (!hasVisiblePathNode) {
            resetCriticalPath();
        } else {
            const limit = criticalPathState.currentStep > 0
                ? Math.min(criticalPathState.currentStep, criticalPathState.path.length)
                : criticalPathState.path.length;
            criticalPathState.path.slice(0, limit).forEach(id => {
                const gNode = nodeById.get(id);
                if (gNode) gNode._criticalPathState = 'active';
            });
        }
    }

    if (whatIfState.active) {
        const sourceIds = (whatIfState.sourceNodes || []).map(n => n.id);
        const hasVisibleWhatIf = sourceIds.some(id => nodeById.has(id))
            || [...whatIfState.unblockedNodes].some(id => nodeById.has(id));
        if (!hasVisibleWhatIf) {
            resetWhatIf();
        } else {
            sourceIds.forEach(id => {
                const gNode = nodeById.get(id);
                if (gNode) gNode._whatIfState = 'closing';
            });
            whatIfState.unblockedNodes.forEach(id => {
                const gNode = nodeById.get(id);
                if (gNode) gNode._whatIfState = 'unblocked';
            });
        }
    }

    if (dependencyConeState.active) {
        const hasVisibleConeNode = [...dependencyConeState.nodeIds].some(id => nodeById.has(id));
        if (!hasVisibleConeNode) {
            resetDependencyCone();
        } else {
            applyDependencyConeToGraphData(graphData);
        }
    }

    if (!cycleNavigatorState.active && !criticalPathState.active && !whatIfState.active && !dependencyConeState.active && !store.selectedNode && store.highlightedNodes.size > 0) {
        for (const id of [...store.highlightedNodes]) {
            if (!nodeById.has(id)) store.highlightedNodes.delete(id);
        }
        if (store.highlightedNodes.size === 0) {
            store.highlightedLinks.clear();
        }
    }
}

function countVisibleGraphNodes(graphData) {
    const nodes = graphData?.nodes || [];
    let open = 0;
    let closed = 0;
    for (const n of nodes) {
        if (isResolvedIssue(n)) closed++;
        else open++;
    }
    return {
        visibleCount: nodes.length,
        openCount: open,
        closedCount: closed,
        totalCount: store.issues.length
    };
}

export function setFilter(key, value) {
    if (timeTravelState.active) {
        stopTimeTravel();
    }
    store.filters[key] = value;
    if (key === 'status' && value === 'closed') {
        store.filters.showClosed = true;
    } else if (key === 'showClosed' && !value && store.filters.status === 'closed') {
        store.filters.status = null;
    }
    if (key === 'labels') {
        labelClusterState.activeLabel = Array.isArray(value) && value.length === 1 ? value[0] : null;
    }
    const graphData = prepareGraphData();
    store.graph.graphData(graphData);
    syncOverlaysAfterGraphRebuild(graphData);
    if (labelClusterState.active && labelLegendEl) {
        showLabelLegend();
    }
    dispatchEvent('filterChange', { filters: { ...store.filters }, ...countVisibleGraphNodes(graphData) });
}

export function clearFilters() {
    if (timeTravelState.active) {
        stopTimeTravel();
    }
    store.filters = {
        status: null,
        priority: null,
        labels: [],
        repo: null,
        search: '',
        showClosed: true  // Reset to showing all issues
    };
    labelClusterState.activeLabel = null;
    const graphData = prepareGraphData();
    store.graph.graphData(graphData);
    syncOverlaysAfterGraphRebuild(graphData);
    if (labelClusterState.active && labelLegendEl) {
        showLabelLegend();
    }
    dispatchEvent('filterChange', { filters: { ...store.filters }, ...countVisibleGraphNodes(graphData) });
}

export function search(term) {
    setFilter('search', term);
}

// ============================================================================
// VIEW CONTROLS
// ============================================================================

export function focusNode(nodeId, zoom = 2) {
    if (!store.graph) return;
    const graphData = store.graph.graphData();
    const node = graphData?.nodes?.find(n => n.id === nodeId);
    if (node) {
        if (Number.isFinite(node.x) && Number.isFinite(node.y)) {
            store.graph.centerAt(node.x, node.y, 500);
            store.graph.zoom(zoom, 500);
        }
        selectNode(node);
    }
}

export function zoomToFit(padding = 50) {
    store.graph.zoomToFit(400, padding);
}

export function resetView() {
    store.checkDepPicking = false;
    resetWhatIf({ force: true });
    resetCriticalPath();
    resetCycleNavigator();
    resetDependencyCone();
    clearSelection();
    clearFilters();
    store.graph.centerAt(0, 0, 500);
    store.graph.zoom(1, 500);
}

export function setViewMode(mode, presetOverride = null) {
    if (mode === 'galaxy') {
        mode = VIEW_MODES.LABEL_GALAXY;
    }
    store.viewMode = mode;

    const modeToPreset = {
        [VIEW_MODES.FORCE]: 'force',
        [VIEW_MODES.HIERARCHY]: 'compact',
        [VIEW_MODES.RADIAL]: 'radial',
        [VIEW_MODES.CLUSTER]: 'cluster',
        [VIEW_MODES.LABEL_GALAXY]: 'galaxy'
    };
    store.currentPreset = presetOverride || modeToPreset[mode] || 'force';

    // Deactivate galaxy mode if switching away from it
    if (mode !== VIEW_MODES.LABEL_GALAXY && labelClusterState.active) {
        deactivateLabelGalaxy();
        store.graph?.d3Force('collision', null);
    }

    // Apply layout forces based on mode
    switch (mode) {
        case VIEW_MODES.HIERARCHY:
            applyHierarchyLayout();
            break;
        case VIEW_MODES.RADIAL:
            applyRadialLayout();
            break;
        case VIEW_MODES.CLUSTER:
            applyClusterLayout();
            break;
        case VIEW_MODES.LABEL_GALAXY:
            applyLabelGalaxyLayout();
            break;
        default:
            applyForceLayout();
    }

    dispatchEvent('viewModeChange', { mode, preset: store.currentPreset });
}

/**
 * Deactivate label galaxy mode (bv-qpt0)
 */
function deactivateLabelGalaxy() {
    labelClusterState.active = false;
    labelClusterState.clusterHulls.clear();
    hideLabelLegend();
    refreshGraph();
    dispatchEvent('labelGalaxyDeactivated');
}

function applyForceLayout() {
    store.graph
        .d3Force('x', null)
        .d3Force('y', null)
        .d3Force('charge', d3.forceManyBody().strength(store.config.chargeStrength))
        .d3ReheatSimulation();
}

function applyHierarchyLayout() {
    // Top-down hierarchy based on critical path depth
    store.graph
        .d3Force('x', d3.forceX(0).strength(0.1))
        .d3Force('y', d3.forceY(node => (node.criticalDepth || 0) * 100).strength(0.3))
        .d3Force('charge', d3.forceManyBody().strength(-50))
        .d3ReheatSimulation();
}

function applyRadialLayout() {
    // Radial layout from selected node or center
    const center = store.selectedNode || { x: 0, y: 0 };
    store.graph
        .d3Force('x', d3.forceX(center.x).strength(0.05))
        .d3Force('y', d3.forceY(center.y).strength(0.05))
        .d3Force('radial', d3.forceRadial(
            node => (node.criticalDepth || 0) * 80,
            center.x,
            center.y
        ).strength(0.5))
        .d3ReheatSimulation();
}

function applyClusterLayout() {
    // Cluster by status
    const statusPositions = {
        open: { x: -200, y: 0 },
        in_progress: { x: 0, y: -150 },
        blocked: { x: 200, y: 0 },
        closed: { x: 0, y: 150 }
    };

    store.graph
        .d3Force('x', d3.forceX(node => statusPositions[node.status]?.x || 0).strength(0.3))
        .d3Force('y', d3.forceY(node => statusPositions[node.status]?.y || 0).strength(0.3))
        .d3Force('charge', d3.forceManyBody().strength(-30))
        .d3ReheatSimulation();
}

/**
 * Apply label-based galaxy layout (bv-qpt0)
 * Groups nodes by their primary label into distinct clusters
 */
function applyLabelGalaxyLayout() {
    // Build label colors and centers if not already done
    if (labelClusterState.labelColorMap.size === 0) {
        buildLabelColorMap();
    }

    labelClusterState.active = true;

    // Apply forces to pull nodes toward their label's center
    store.graph
        .d3Force('x', d3.forceX(node => {
            const label = getPrimaryLabel(node);
            const center = labelClusterState.labelCenters.get(label);
            return center ? center.x : 0;
        }).strength(0.4))
        .d3Force('y', d3.forceY(node => {
            const label = getPrimaryLabel(node);
            const center = labelClusterState.labelCenters.get(label);
            return center ? center.y : 0;
        }).strength(0.4))
        .d3Force('charge', d3.forceManyBody().strength(-40))
        .d3Force('collision', d3.forceCollide().radius(node => getNodeSize(node) + 8))
        .d3ReheatSimulation();

    // Show label legend
    showLabelLegend();

    // Schedule hull computation after layout settles
    setTimeout(() => {
        computeClusterHulls();
        refreshGraph();
    }, 1000);

    dispatchEvent('labelGalaxyActivated', {
        labels: labelClusterState.labels,
        colorMap: Object.fromEntries(labelClusterState.labelColorMap)
    });
}

// ============================================================================
// LAYOUT PRESETS API (bv-97)
// ============================================================================

/**
 * Apply a layout preset by name.
 * Presets configure force simulation parameters and optionally set view mode.
 * @param {string} presetName - One of: 'force', 'compact', 'spread', 'orthogonal', 'radial', 'cluster'
 * @returns {boolean} True if preset was applied successfully
 */
function applyPreset(presetName) {
    const preset = LAYOUT_PRESETS[presetName];
    if (!preset) {
        console.warn(`[Graph] Unknown preset: ${presetName}. Available: ${Object.keys(LAYOUT_PRESETS).join(', ')}`);
        return false;
    }

    console.log(`[Graph] Applying preset: ${preset.name} (${presetName})`);

    // Update store config with preset values
    Object.assign(store.config, preset.config);
    store.currentPreset = presetName;
    if (timeTravelState.active) {
        timeTravelState.warmupTicks = store.config.warmupTicks;
    }
    store.graph
        .warmupTicks(timeTravelState.active ? 0 : store.config.warmupTicks)
        .cooldownTicks(store.config.cooldownTicks)
        .d3Force('link', d3.forceLink()
            .id(d => d.id)
            .distance(getLinkDistance))
        .d3Force('center', d3.forceCenter())
        .d3Force('collision', null);

    // Update view mode and sync toolbar state
    const targetMode = preset.viewMode || VIEW_MODES.FORCE;
    setViewMode(targetMode, presetName);

    // Apply custom forces if defined, or standard force config for force-mode presets
    if (preset.customForces) {
        applyCustomForces(preset.customForces);
    } else if (targetMode === VIEW_MODES.FORCE) {
        store.graph
            .d3Force('charge', d3.forceManyBody()
                .strength(store.config.chargeStrength))
            .d3Force('x', d3.forceX()
                .strength(store.config.centerStrength))
            .d3Force('y', d3.forceY()
                .strength(store.config.centerStrength))
            .d3ReheatSimulation();
    }

    dispatchEvent('presetApplied', { preset: presetName, config: preset });
    return true;
}

/**
 * Apply custom forces for special presets like orthogonal
 */
function applyCustomForces(customForces) {
    if (customForces.gridSize && customForces.gridStrength) {
        // Orthogonal: snap to grid
        const gridSize = customForces.gridSize;
        const gridStrength = customForces.gridStrength;

        store.graph
            .d3Force('x', d3.forceX(node => {
                // Snap to nearest grid column
                return Math.round(node.x / gridSize) * gridSize;
            }).strength(gridStrength))
            .d3Force('y', d3.forceY(node => {
                // Snap to nearest grid row, with depth offset
                const depth = node.criticalDepth || 0;
                return depth * gridSize + Math.round(node.y / gridSize) * gridSize;
            }).strength(gridStrength))
            .d3Force('charge', d3.forceManyBody().strength(store.config.chargeStrength))
            .d3ReheatSimulation();
    } else if (customForces.yStrength && customForces.depthSpacing) {
        // Compact DAG: emphasize vertical hierarchy
        store.graph
            .d3Force('x', d3.forceX(0).strength(0.15))
            .d3Force('y', d3.forceY(node => (node.criticalDepth || 0) * customForces.depthSpacing)
                .strength(customForces.yStrength))
            .d3Force('charge', d3.forceManyBody().strength(store.config.chargeStrength))
            .d3ReheatSimulation();
    }
}

/**
 * Get available layout presets
 * @returns {Object} Map of preset name -> preset info
 */
function getLayoutPresets() {
    return { ...LAYOUT_PRESETS };
}

/**
 * Get current preset name
 * @returns {string} Current preset name
 */
function getCurrentPreset() {
    return store.currentPreset;
}

/**
 * Get the default export preset name
 * @returns {string} Default preset for exports
 */
function getDefaultExportPreset() {
    return DEFAULT_EXPORT_PRESET;
}

// ============================================================================
// KEYBOARD SHORTCUTS
// ============================================================================

let keyboardShortcutHandler = null;

function isElementVisibleInLayout(el) {
    if (!el || !el.isConnected) return false;
    if (el.getClientRects().length === 0) return false;
    const rect = el.getBoundingClientRect();
    if (rect.width <= 0 || rect.height <= 0) return false;
    return getComputedStyle(el).visibility !== 'hidden';
}

function isInteractiveControlTarget(target) {
    if (!target || typeof target !== 'object') return false;
    if (['BUTTON', 'A', 'SELECT', 'SUMMARY'].includes(target.tagName)) {
        return true;
    }
    if (typeof target.closest === 'function' && target.closest('button, a, select, summary, [role="button"]')) {
        return true;
    }
    return false;
}

function isGraphShortcutScopeActive(e) {
    const target = e.target;
    if (target && (['INPUT', 'TEXTAREA', 'SELECT'].includes(target.tagName) || target.isContentEditable)) {
        return false;
    }
    if (e.key === ' ' && isInteractiveControlTarget(target)) {
        return false;
    }

    const container = store.container || document.getElementById('graph-container');
    if (!isElementVisibleInLayout(container)) {
        return false;
    }

    const rootEl = document.querySelector('[x-data]');
    const appData = (typeof window !== 'undefined' && typeof window.Alpine !== 'undefined' && rootEl && typeof window.Alpine.$data === 'function')
        ? window.Alpine.$data(rootEl)
        : null;
    if (appData) {
        if (appData.view && appData.view !== 'graph') {
            return false;
        }
        if (appData.selectedIssue || appData.showKeyboardHelp || appData.globalError || appData.showTriageModal || appData.showDiagnostics || appData.graphContextMenu?.open) {
            return false;
        }
    }

    const modalSelectors = [
        '[x-show="selectedIssue"]',
        '[x-show="showKeyboardHelp"]',
        '[x-show="globalError"]',
        '[x-show="showTriageModal"]',
        '[x-show="showDiagnostics"]'
    ];
    for (const sel of modalSelectors) {
        const modalEl = document.querySelector(sel);
        if (isElementVisibleInLayout(modalEl)) {
            return false;
        }
    }

    return true;
}

function setupKeyboardShortcuts() {
    if (keyboardShortcutHandler) {
        document.removeEventListener('keydown', keyboardShortcutHandler);
    }

    keyboardShortcutHandler = (e) => {
        if (!isGraphShortcutScopeActive(e)) return;

        switch (e.key) {
            case 'Escape':
                if (whatIfState.active) {
                    e.stopPropagation();
                    resetWhatIf();
                } else if (criticalPathState.active) {
                    e.stopPropagation();
                    resetCriticalPath();
                } else if (cycleNavigatorState.active) {
                    e.stopPropagation();
                    resetCycleNavigator();
                } else if (dependencyConeState.active) {
                    e.stopPropagation();
                    resetDependencyCone();
                } else {
                    clearSelection();
                }
                break;
            case 'w':
                // What-if on selected node
                if (store.selectedNode) {
                    performWhatIf(store.selectedNode);
                }
                break;
            case 'f':
                if (e.ctrlKey || e.metaKey) {
                    e.preventDefault();
                    dispatchEvent('searchRequest');
                }
                break;
            case 'r':
                resetView();
                break;
            case '1':
                setViewMode(VIEW_MODES.FORCE);
                break;
            case '2':
                setViewMode(VIEW_MODES.HIERARCHY);
                break;
            case '3':
                setViewMode(VIEW_MODES.RADIAL);
                break;
            case '4':
                setViewMode(VIEW_MODES.CLUSTER);
                break;
            case '5':
                setViewMode(VIEW_MODES.LABEL_GALAXY);
                break;
            case 'c':
                toggleCriticalPath();
                break;
            case 'C':
                toggleDependencyCone();
                break;
            case 'y':
                toggleCycleNavigator();
                break;
            case 'h':
                // Toggle heatmap mode
                toggleHeatmap();
                break;
            case '[':
                // Previous cycle or critical path [1/N]
                if (cycleNavigatorState.active) {
                    prevCycle();
                } else if (criticalPathState.active) {
                    prevCriticalPath();
                }
                break;
            case ']':
                // Next cycle or critical path [1/N]
                if (cycleNavigatorState.active) {
                    nextCycle();
                } else if (criticalPathState.active) {
                    nextCriticalPath();
                }
                break;
            case '?':
                dispatchEvent('helpRequest');
                break;
            case 't':
                // Toggle time-travel mode (bv-z38b)
                if (timeTravelState.history) {
                    toggleTimeTravel();
                }
                break;
            case ' ':
                // Space to play/pause time-travel (when not focused on an interactive control)
                if (timeTravelState.active && !isInteractiveControlTarget(e.target)) {
                    e.preventDefault();
                    togglePlay();
                }
                break;
            case 'ArrowLeft':
            case 'ArrowRight':
                // Navigate nodes by priority order (P0 first, then by PageRank within priority)
                e.preventDefault();
                navigateByPriority(e.key === 'ArrowRight' ? 1 : -1);
                break;
            case 'ArrowUp':
            case 'ArrowDown':
                // Navigate between k-core cliques (up = higher cohesion, down = lower)
                e.preventDefault();
                navigateByKCore(e.key === 'ArrowUp' ? 1 : -1);
                break;
        }
    };
    document.addEventListener('keydown', keyboardShortcutHandler);
}

/**
 * Navigate nodes by priority order (P0 first, then P1, etc., sorted by PageRank within each priority)
 * @param {number} direction - 1 for next, -1 for previous
 */
function navigateByPriority(direction) {
    const graphData = store.graph?.graphData();
    if (!graphData?.nodes?.length) return;

    // Sort nodes by priority (ascending), then by PageRank (descending)
    const sortedNodes = [...graphData.nodes]
        .filter(n => !isResolvedIssue(n))
        .sort((a, b) => {
            const pDiff = (a.priority ?? 99) - (b.priority ?? 99);
            if (pDiff !== 0) return pDiff;
            return (b.pagerank || 0) - (a.pagerank || 0);
        });

    if (sortedNodes.length === 0) return;

    // Find current position
    const currentId = store.selectedNode?.id;
    let currentIdx = currentId ? sortedNodes.findIndex(n => n.id === currentId) : -1;

    // Calculate new index with wrapping
    let newIdx;
    if (currentIdx === -1) {
        newIdx = direction > 0 ? 0 : sortedNodes.length - 1;
    } else {
        newIdx = (currentIdx + direction + sortedNodes.length) % sortedNodes.length;
    }

    // Select and center on the node
    const targetNode = sortedNodes[newIdx];
    selectAndCenterNode(targetNode);
}

/**
 * Navigate between k-core cliques (up = higher k-core, down = lower k-core)
 * @param {number} direction - 1 for higher k-core, -1 for lower k-core
 */
function navigateByKCore(direction) {
    const graphData = store.graph?.graphData();
    if (!graphData?.nodes?.length) return;

    // Get all unique k-core values and sort them
    const kCores = [...new Set(graphData.nodes.map(n => n.kcore ?? 0))].sort((a, b) => a - b);
    if (kCores.length === 0) return;

    // Find current k-core level
    const currentKCore = store.selectedNode?.kcore ?? 0;
    let currentKCoreIdx = kCores.indexOf(currentKCore);
    if (currentKCoreIdx === -1) currentKCoreIdx = 0;

    // Get nodes in the current k-core level
    const currentKCoreNodes = graphData.nodes.filter(n => (n.kcore ?? 0) === kCores[currentKCoreIdx]);
    const currentIdx = currentKCoreNodes.findIndex(n => n.id === store.selectedNode?.id);

    let targetNode;

    if (direction > 0) {
        // Up: Try next node in current k-core, or jump to next higher k-core
        if (currentIdx >= 0 && currentIdx < currentKCoreNodes.length - 1) {
            targetNode = currentKCoreNodes[currentIdx + 1];
        } else if (currentKCoreIdx < kCores.length - 1) {
            // Jump to first node in next higher k-core
            const higherKCoreNodes = graphData.nodes.filter(n => (n.kcore ?? 0) === kCores[currentKCoreIdx + 1]);
            targetNode = higherKCoreNodes[0];
        } else {
            // Wrap to first node in lowest k-core
            const lowestKCoreNodes = graphData.nodes.filter(n => (n.kcore ?? 0) === kCores[0]);
            targetNode = lowestKCoreNodes[0];
        }
    } else {
        // Down: Try previous node in current k-core, or jump to next lower k-core
        if (currentIdx > 0) {
            targetNode = currentKCoreNodes[currentIdx - 1];
        } else if (currentKCoreIdx > 0) {
            // Jump to last node in next lower k-core
            const lowerKCoreNodes = graphData.nodes.filter(n => (n.kcore ?? 0) === kCores[currentKCoreIdx - 1]);
            targetNode = lowerKCoreNodes[lowerKCoreNodes.length - 1];
        } else {
            // Wrap to last node in highest k-core
            const highestKCoreNodes = graphData.nodes.filter(n => (n.kcore ?? 0) === kCores[kCores.length - 1]);
            targetNode = highestKCoreNodes[highestKCoreNodes.length - 1];
        }
    }

    if (targetNode) {
        selectAndCenterNode(targetNode);
    }
}

/**
 * Select a node and center the view on it
 */
function selectAndCenterNode(node) {
    if (!node || !store.graph) return;

    // Select the node
    store.selectedNode = node;
    updateConnectedNodes(node);
    showTooltip(node);

    // Center and zoom to the node
    if (typeof node.x === 'number' && typeof node.y === 'number') {
        store.graph.centerAt(node.x, node.y, 500);
    }

    // Refresh display
    refreshGraph();

    // Dispatch selection event
    dispatchEvent('nodeSelect', { node });
}

// ============================================================================
// TOOLTIPS
// ============================================================================

let tooltipEl = null;

/**
 * Format topological slack in dependency hops (never calendar days/hours)
 */
export function formatSlack(slack) {
    if (slack === undefined || slack === null || typeof slack !== 'number' || !isFinite(slack) || isNaN(slack)) return '—';
    const hops = Math.max(0, Math.round(slack));
    if (hops === 0) return '0 hops (critical)';
    if (hops === 1) return '1 hop';
    return `${hops} hops`;
}

function applyThemePalette(isDark) {
    currentGraphDarkMode = Boolean(isDark);
    const palette = currentGraphDarkMode ? THEME_PALETTES.dark : THEME_PALETTES.light;
    THEME.bg = palette.bg;
    THEME.bgSecondary = palette.bgSecondary;
    THEME.bgTertiary = palette.bgTertiary;
    THEME.fg = palette.fg;
    THEME.fgMuted = palette.fgMuted;
    THEME.link.default = palette.linkDefault;
}

export function getGraphTheme() {
    return currentGraphDarkMode ? 'dark' : 'light';
}

export function setGraphTheme(isDark) {
    applyThemePalette(isDark);
    if (store.graph) {
        store.graph.backgroundColor(THEME.bg);
    }
    if (tooltipEl) {
        tooltipEl.style.background = THEME.bgSecondary;
        tooltipEl.style.color = THEME.fg;
    }
    if (labelClusterState.active && labelLegendEl) {
        showLabelLegend();
    }
    if (typeof document !== 'undefined' && document.getElementById('bv-time-travel-styles')) {
        applyTimeTravelStyles();
    }
    refreshGraph();
}

function showTooltip(node) {
    const mountTarget = store.container || document.body;
    if (!tooltipEl) {
        tooltipEl = document.createElement('div');
        tooltipEl.className = 'bv-graph-tooltip';
    }
    if (tooltipEl.parentElement !== mountTarget) {
        mountTarget.appendChild(tooltipEl);
    }
    tooltipEl.style.cssText = `
        position: ${store.container ? 'absolute' : 'fixed'};
        background: ${THEME.bgSecondary};
        color: ${THEME.fg};
        padding: 12px 16px;
        border-radius: 8px;
        border: 1px solid ${THEME.accent.purple};
        font-family: 'JetBrains Mono', monospace;
        font-size: 12px;
        max-width: 320px;
        pointer-events: none;
        z-index: 25;
        box-shadow: 0 8px 32px rgba(0,0,0,0.28);
        transition: opacity 0.15s;
    `;

    const icon = TYPE_ICONS[node.type] || TYPE_ICONS.default;
    const statusColor = THEME.status[node.status];
    const priorityColor = THEME.priority[node.priority];

    // Safe number formatter for metrics
    const safeMetric = (val, decimals = 1, suffix = '') => {
        if (val === undefined || val === null || typeof val !== 'number' || !isFinite(val)) return '—';
        return val.toFixed(decimals) + suffix;
    };

    // Build extended metrics section
    const extendedMetrics = [];
    if (node.betweenness !== undefined && isFinite(node.betweenness)) {
        extendedMetrics.push(`<span title="Bottleneck centrality">Between: ${safeMetric(node.betweenness * 100, 1, '%')}</span>`);
    }
    if (node.kcore !== undefined && isFinite(node.kcore)) {
        extendedMetrics.push(`<span title="Cluster cohesion level">K-core: ${node.kcore}</span>`);
    }
    if (node.hitsHub !== undefined || node.hitsAuth !== undefined) {
        const hub = safeMetric(node.hitsHub !== undefined ? node.hitsHub * 100 : undefined, 0);
        const auth = safeMetric(node.hitsAuth !== undefined ? node.hitsAuth * 100 : undefined, 0);
        extendedMetrics.push(`<span title="Hub/Authority scores">HITS: ${hub}/${auth}</span>`);
    }
    if (node.eigenvector !== undefined && isFinite(node.eigenvector)) {
        extendedMetrics.push(`<span title="Influence centrality">Eigen: ${safeMetric(node.eigenvector * 100, 1, '%')}</span>`);
    }
    if (store.metrics.hasCycles) {
        extendedMetrics.push(`<span style="color: ${THEME.fgMuted}" title="Topological slack requires an acyclic DAG">Slack: N/A (cycle)</span>`);
    } else if (typeof node.slack === 'number' && Number.isFinite(node.slack)) {
        const slackColor = node.slack === 0 ? THEME.priority[0] : node.slack <= 2 ? THEME.priority[1] : THEME.fgMuted;
        extendedMetrics.push(`<span style="color: ${slackColor}" title="Topological float in dependency hops">Slack: ${formatSlack(node.slack)}</span>`);
    }
    if (node.isArticulationPoint) {
        extendedMetrics.push(`<span style="color: ${THEME.accent.orange || THEME.priority[1]}" title="Removing this node disconnects the dependency graph">Cut vertex: Yes</span>`);
    }

    tooltipEl.innerHTML = `
        <div style="font-weight: 600; margin-bottom: 8px; color: ${THEME.accent.cyan}">
            ${icon} ${escapeHtml(node.id)}
        </div>
        <div style="margin-bottom: 8px; line-height: 1.4;">
            ${escapeHtml(node.title)}
        </div>
        <div style="display: flex; gap: 8px; flex-wrap: wrap; margin-bottom: 8px;">
            <span style="background: ${statusColor}; color: ${THEME.bg}; padding: 2px 8px; border-radius: 4px; font-size: 10px; text-transform: uppercase;">
                ${escapeHtml(node.status)}
            </span>
            <span style="color: ${priorityColor}; font-weight: 600;">
                P${escapeHtml(String(node.priority))}
            </span>
            ${node.sourceRepo ? `<span style="background: ${THEME.bgTertiary}; color: ${THEME.accent.cyan}; padding: 2px 6px; border-radius: 4px; font-size: 10px; font-weight: 600;">${escapeHtml(node.sourceRepo)}</span>` : ''}
            ${node.isArticulationPoint ? `<span style="background: ${(THEME.accent.orange || '#ffb86c')}33; color: ${THEME.accent.orange || '#ffb86c'}; padding: 2px 6px; border-radius: 4px; font-size: 10px; font-weight: 600;">ARTICULATION</span>` : ''}
        </div>
        <div style="font-size: 10px; color: ${THEME.fgMuted}; display: grid; grid-template-columns: 1fr 1fr; gap: 4px;">
            <span>Blockers: ${node.blockerCount ?? 0}</span>
            <span>Dependents: ${node.dependentCount ?? 0}</span>
            <span>PageRank: ${safeMetric(node.pagerank !== undefined ? node.pagerank * 100 : undefined, 1, '%')}</span>
            <span>Depth: ${node.criticalDepth ?? 0}</span>
            ${extendedMetrics.join('')}
        </div>
        ${node.labels?.length ? `
            <div style="margin-top: 8px; display: flex; gap: 4px; flex-wrap: wrap;">
                ${node.labels.map(l => `<span style="background: ${THEME.bgTertiary}; padding: 2px 6px; border-radius: 4px; font-size: 10px;">${escapeHtml(l)}</span>`).join('')}
            </div>
        ` : ''}
    `;

    tooltipEl.style.opacity = '1';
    tooltipEl.style.display = 'block';

    // Position near cursor
    document.addEventListener('mousemove', positionTooltip);
}

function positionTooltip(e) {
    if (!tooltipEl) return;
    if (store.container) {
        const rect = store.container.getBoundingClientRect();
        const relX = e.clientX - rect.left + 15;
        const relY = e.clientY - rect.top + 15;
        const maxLeft = Math.max(8, rect.width - 332);
        const maxTop = Math.max(8, rect.height - 200);
        tooltipEl.style.left = `${Math.max(8, Math.min(relX, maxLeft))}px`;
        tooltipEl.style.top = `${Math.max(8, Math.min(relY, maxTop))}px`;
        return;
    }
    const x = e.clientX + 15;
    const y = e.clientY + 15;
    tooltipEl.style.left = `${Math.min(x, window.innerWidth - 340)}px`;
    tooltipEl.style.top = `${Math.min(y, window.innerHeight - 200)}px`;
}

export function hideTooltip() {
    if (tooltipEl) {
        tooltipEl.style.opacity = '0';
        document.removeEventListener('mousemove', positionTooltip);
        setTimeout(() => {
            if (tooltipEl) tooltipEl.style.display = 'none';
        }, 150);
    }
}

// ============================================================================
// LABEL LEGEND (bv-qpt0)
// ============================================================================

let labelLegendEl = null;

/**
 * Show the label legend for galaxy view
 */
function showLabelLegend() {
    if (!labelClusterState.showLegend) return;

    // Remove existing legend if any
    hideLabelLegend();

    labelLegendEl = document.createElement('div');
    labelLegendEl.className = 'bv-label-legend';
    labelLegendEl.style.cssText = `
        position: ${store.container ? 'absolute' : 'fixed'};
        top: 156px;
        left: 16px;
        background: ${THEME.bgSecondary}ee;
        color: ${THEME.fg};
        padding: 12px 16px;
        border-radius: 8px;
        border: 1px solid ${THEME.accent.purple};
        font-family: 'JetBrains Mono', monospace;
        font-size: 11px;
        max-width: 240px;
        max-height: calc(100% - 310px);
        overflow-y: auto;
        z-index: 25;
        box-shadow: 0 4px 24px rgba(0,0,0,0.25);
        backdrop-filter: blur(8px);
    `;

    const title = document.createElement('div');
    title.style.cssText = `
        font-weight: 600;
        margin-bottom: 10px;
        color: ${THEME.accent.cyan};
        display: flex;
        justify-content: space-between;
        align-items: center;
    `;
    const heading = document.createElement('span');
    heading.textContent = 'Labels';
    // A listener, not an onclick attribute: the page's CSP has no
    // 'unsafe-inline' in script-src, so inline handlers never run.
    const close = document.createElement('span');
    close.textContent = '×';
    close.style.cssText = 'cursor: pointer; opacity: 0.6;';
    close.addEventListener('click', hideLabelLegend);
    title.append(heading, close);
    labelLegendEl.appendChild(title);

    // Add label items
    labelClusterState.labels.forEach(label => {
        const color = labelClusterState.labelColorMap.get(label);
        const item = document.createElement('div');
        item.style.cssText = `
            display: flex;
            align-items: center;
            gap: 8px;
            padding: 4px 8px;
            margin: 2px 0;
            border-radius: 4px;
            cursor: pointer;
            transition: background 0.15s;
        `;
        item.innerHTML = `
            <span style="width: 12px; height: 12px; border-radius: 50%; background: ${color}; flex-shrink: 0;"></span>
            <span style="flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;">${escapeHtml(label)}</span>
            <span style="opacity: 0.5; font-size: 10px;">${countNodesWithLabel(label)}</span>
        `;

        // Hover effect
        item.addEventListener('mouseenter', () => {
            item.style.background = THEME.bgTertiary;
        });
        item.addEventListener('mouseleave', () => {
            item.style.background = 'transparent';
        });

        // Click to filter
        item.addEventListener('click', () => {
            if (labelClusterState.activeLabel === label) {
                // Clear filter
                labelClusterState.activeLabel = null;
                clearFilters();
            } else {
                // Filter to this label
                labelClusterState.activeLabel = label;
                if (label === '(unlabeled)') {
                    // Filter to issues with no labels
                    setFilter('labels', []);
                    // Custom filter for unlabeled
                    const graphData = prepareGraphData();
                    const filteredNodes = graphData.nodes.filter(n => !n.labels || n.labels.length === 0);
                    highlightNodes(filteredNodes.map(n => n.id));
                } else {
                    setFilter('labels', [label]);
                }
            }
        });

        labelLegendEl.appendChild(item);
    });

    // Add keyboard hint
    const hint = document.createElement('div');
    hint.style.cssText = `
        margin-top: 10px;
        padding-top: 8px;
        border-top: 1px solid ${THEME.bgTertiary};
        color: ${THEME.fgMuted};
        font-size: 10px;
    `;
    hint.textContent = 'Press 5 to toggle galaxy view';
    labelLegendEl.appendChild(hint);

    (store.container || document.body).appendChild(labelLegendEl);
}

/**
 * Hide the label legend
 */
function hideLabelLegend() {
    if (labelLegendEl) {
        labelLegendEl.remove();
        labelLegendEl = null;
    }
    labelClusterState.activeLabel = null;
}

/**
 * Count nodes with a specific label
 */
function countNodesWithLabel(label) {
    if (!store.graph) return 0;
    const graphData = store.graph.graphData();
    return graphData.nodes.filter(n => {
        if (label === '(unlabeled)') {
            return !n.labels || n.labels.length === 0;
        }
        return n.labels && n.labels.includes(label);
    }).length;
}

/**
 * Toggle label legend visibility
 */
export function toggleLabelLegend() {
    if (labelLegendEl) {
        hideLabelLegend();
    } else {
        showLabelLegend();
    }
}

// Expose hideLabelLegend globally for the close button
if (typeof window !== 'undefined') {
    window.bvGraph = window.bvGraph || {};
    window.bvGraph.hideLabelLegend = hideLabelLegend;
}

// ============================================================================
// UTILITIES
// ============================================================================

function dispatchEvent(name, detail = {}) {
    document.dispatchEvent(new CustomEvent(`bv-graph:${name}`, { detail, bubbles: true }));
}

let _graphToastSequence = 0;

/**
 * Show a toast notification (delegates to parent viewer)
 */
function showToast(message, type = 'info') {
    _graphToastSequence += 1;
    // Dispatch event for parent viewer to handle
    window.dispatchEvent(new CustomEvent('show-toast', {
        detail: { message, type, id: `${Date.now()}-g${_graphToastSequence}` }
    }));
    // Also log for debugging
    console.log(`[bv-graph] Toast (${type}): ${message}`);
}

function truncate(str, maxLen) {
    if (!str || str.length <= maxLen) return str;
    return str.substring(0, maxLen - 3) + '...';
}

function escapeHtml(str) {
    if (!str) return '';
    const div = document.createElement('div');
    div.textContent = str;
    return div.innerHTML;
}

// ============================================================================
// PUBLIC API
// ============================================================================

export function getGraph() {
    return store.graph;
}

export function getWasmGraph() {
    return store.wasmGraph;
}

export function getActiveWasmGraph() {
    return store.activeWasmGraph;
}

export function getWhatIfWasmGraph() {
    return store.whatIfWasmGraph;
}

export function isWasmReady() {
    return store.wasmReady;
}

export function getMetrics() {
    return { ...store.metrics };
}

export function getSelectedNode() {
    return store.selectedNode;
}

export function getFilters() {
    return { ...store.filters };
}

export function getConfig() {
    return { ...store.config };
}

export function setConfig(key, value) {
    store.config[key] = value;
    refreshGraph();
}

export function cleanup() {
    resetTimeTravel();
    resetWhatIf();
    resetCriticalPath();
    resetCycleNavigator();
    resetDependencyCone();
    hideLabelLegend();
    store.resizeObserver?.disconnect();
    store.resizeObserver = null;
    store.graph?.pauseAnimation();
    hideTooltip();
    if (tooltipEl) {
        tooltipEl.remove();
        tooltipEl = null;
    }
    if (store.whatIfWasmGraph) {
        store.whatIfWasmGraph.free();
        store.whatIfWasmGraph = null;
    }
    store.whatIfModel = null;
    if (store.activeWasmGraph) {
        store.activeWasmGraph.free();
        store.activeWasmGraph = null;
    }
    if (store.wasmGraph) {
        store.wasmGraph.free();
        store.wasmGraph = null;
    }
    if (store.animationFrame) {
        cancelAnimationFrame(store.animationFrame);
    }
    store.graph = null;
}

// Note: Cycle navigator functions are already exported at their definitions
// Label helper functions (not toggleLabelLegend which is already exported)
export { getLabelColor, getPrimaryLabel };

/**
 * Get label cluster state for external access
 */
export function getLabelClusterState() {
    return {
        active: labelClusterState.active,
        labels: [...labelClusterState.labels],
        colorMap: Object.fromEntries(labelClusterState.labelColorMap),
        activeLabel: labelClusterState.activeLabel
    };
}

// Export layout preset functions (bv-97)
export {
    applyPreset,
    getLayoutPresets,
    getCurrentPreset,
    getDefaultExportPreset
};

// Export constants
export { THEME, VIEW_MODES, TYPE_ICONS, LABEL_COLORS, LAYOUT_PRESETS };

// ============================================================================
// TIME-TRAVEL ANIMATION (bv-z38b)
// ============================================================================

/**
 * Time-travel state for graph history animation
 */
const timeTravelState = {
    active: false,
    playing: false,
    currentIdx: 0,
    history: null,        // { commits: [{sha, date, beads_added[], beads_closed[], ...}] }
    speed: 1,
    animationFrame: null,
    transitionFrame: null,
    autoPauseRedraw: true,
    warmupTicks: 0,
    lastFrameTime: 0,
    originalNodes: [],    // Snapshot of nodes before time-travel
    originalLinks: [],    // Snapshot of links before time-travel
    nodeStates: new Map(), // node.id -> { visible, from, startedAt, duration }
    controlsEl: null,
};

function timelineOpacity(state, now = performance.now()) {
    const progress = state.duration ? Math.min(1, Math.max(0, (now - state.startedAt) / state.duration)) : 1;
    return state.from + ((state.visible ? 1 : 0) - state.from) * progress;
}

function stopTimelineTransition() {
    if (timeTravelState.transitionFrame !== null) {
        cancelAnimationFrame(timeTravelState.transitionFrame);
        timeTravelState.transitionFrame = null;
    }
    if (timeTravelState.active && store.graph) {
        store.graph.autoPauseRedraw(timeTravelState.autoPauseRedraw);
    }
}

function redrawTimelineTransition() {
    const now = performance.now();
    const pending = [...timeTravelState.nodeStates.values()].some(state => now < state.startedAt + state.duration);
    if (!timeTravelState.active) {
        stopTimelineTransition();
        return;
    }
    if (!pending) {
        // Allow one final canvas frame to clear the last disappearing pixels.
        timeTravelState.transitionFrame = requestAnimationFrame(stopTimelineTransition);
        return;
    }
    timeTravelState.transitionFrame = requestAnimationFrame(redrawTimelineTransition);
}

/**
 * Initialize time-travel with history data
 * @param {Object} history - History data from --robot-history
 */
export function initTimeTravel(history) {
    resetTimeTravel();
    if (!history || !history.commits || history.commits.length === 0) {
        console.warn('[TimeTravel] No history data provided');
        return false;
    }

    // The exporter supplies Git ancestry order. Author timestamps can be
    // equal or backdated, so sorting them would change lifecycle replay.

    timeTravelState.history = history;
    timeTravelState.currentIdx = 0;
    timeTravelState.active = false;
    timeTravelState.playing = false;

    // Create timeline controls
    createTimelineControls();

    console.log(`[TimeTravel] Initialized with ${history.commits.length} commits`);
    dispatchEvent('timeTravelReady', { commits: history.commits.length });

    return true;
}

/**
 * Create timeline controls UI
 */
function createTimelineControls() {
    // Remove existing controls if any
    if (timeTravelState.controlsEl) {
        timeTravelState.controlsEl.remove();
    }

    const controls = document.createElement('div');
    controls.id = 'time-travel-controls';
    controls.className = 'time-travel-controls';
    controls.innerHTML = `
        <div class="timeline-header">
            <span class="timeline-icon">⏱️</span>
            <span class="timeline-title">Time Travel</span>
            <button class="timeline-close" title="Close">✕</button>
        </div>
        <div class="timeline-content">
            <div class="timeline-buttons">
                <button class="timeline-btn" id="tt-start" title="Go to start">⏮</button>
                <button class="timeline-btn" id="tt-back" title="Previous commit">⏪</button>
                <button class="timeline-btn timeline-btn-primary" id="tt-play" title="Play/Pause">▶️</button>
                <button class="timeline-btn" id="tt-forward" title="Next commit">⏩</button>
                <button class="timeline-btn" id="tt-end" title="Go to end">⏭</button>
            </div>
            <div class="timeline-scrubber">
                <input type="range" id="tt-slider" min="0" max="100" value="0">
            </div>
            <div class="timeline-sprints" aria-label="Sprint boundaries from current definitions"></div>
            <div class="timeline-info">
                <span id="tt-date">--</span>
                <span id="tt-position">0 / 0</span>
            </div>
            <div class="timeline-speed">
                <label>Speed:</label>
                <select id="tt-speed">
                    <option value="0.5">0.5x</option>
                    <option value="1" selected>1x</option>
                    <option value="2">2x</option>
                    <option value="5">5x</option>
                    <option value="10">10x</option>
                </select>
            </div>
        </div>
    `;

    controls.querySelector('#tt-speed').value = String(timeTravelState.speed);
    controls.querySelector('#tt-slider').max = String(timeTravelState.history.commits.length - 1);
    const commits = timeTravelState.history.commits;
    const dated = commits.map((commit, idx) => ({idx, at: Date.parse(commit.date)}))
        .filter(commit => Number.isFinite(commit.at)).sort((a, b) => a.at - b.at || a.idx - b.idx);
    const markers = controls.querySelector('.timeline-sprints');
    for (const sprint of timeTravelState.history.sprints || []) {
        for (const [field, label] of [['start_date', 'Start'], ['end_date', 'End']]) {
            const at = Date.parse(sprint[field]);
            if (!Number.isFinite(at) || !dated.length || at < dated[0].at || at > dated[dated.length - 1].at) continue;
            const target = dated.find(commit => commit.at >= at);
            const button = document.createElement('button');
            button.className = 'timeline-btn';
            button.textContent = `${label}: ${sprint.name}`;
            button.title = `${new Date(at).toISOString()} — nearest following recorded commit; current sprint definition`;
            button.addEventListener('click', () => goToCommit(target.idx));
            markers.appendChild(button);
        }
    }
    markers.hidden = !markers.childElementCount;

    applyTimeTravelStyles();

    // Append controls to graph container
    if (store.container) {
        store.container.appendChild(controls);
    }

    timeTravelState.controlsEl = controls;

    // Setup event listeners
    setupTimeTravelListeners();
}

function applyTimeTravelStyles() {
    let style = document.getElementById('bv-time-travel-styles');
    if (!style) {
        style = document.createElement('style');
        style.id = 'bv-time-travel-styles';
        document.head.appendChild(style);
    }
    style.textContent = `
        .time-travel-controls {
            position: absolute;
            bottom: 20px;
            left: 50%;
            transform: translateX(-50%);
            background: ${THEME.bgSecondary};
            border: 1px solid ${THEME.fgMuted};
            border-radius: 8px;
            padding: 8px 12px;
            z-index: 25;
            min-width: 300px;
            box-shadow: 0 4px 12px rgba(0,0,0,0.3);
            display: none;
        }
        .time-travel-controls.active {
            display: block;
        }
        .timeline-header {
            display: flex;
            align-items: center;
            gap: 8px;
            margin-bottom: 8px;
            padding-bottom: 8px;
            border-bottom: 1px solid ${THEME.fgMuted};
        }
        .timeline-title {
            flex: 1;
            font-weight: 600;
            color: ${THEME.fg};
        }
        .timeline-close {
            background: none;
            border: none;
            color: ${THEME.fgMuted};
            cursor: pointer;
            font-size: 16px;
        }
        .timeline-close:hover {
            color: ${THEME.accent.red};
        }
        .timeline-buttons {
            display: flex;
            justify-content: center;
            gap: 4px;
            margin-bottom: 8px;
        }
        .timeline-btn {
            background: ${THEME.bgTertiary};
            border: 1px solid ${THEME.fgMuted};
            color: ${THEME.fg};
            padding: 4px 8px;
            border-radius: 4px;
            cursor: pointer;
            font-size: 14px;
        }
        .timeline-btn:hover {
            background: ${THEME.accent.purple};
        }
        .timeline-btn-primary {
            background: ${THEME.accent.purple};
        }
        .timeline-scrubber {
            margin-bottom: 8px;
        }
        .timeline-sprints:not([hidden]) {
            display: flex;
            flex-wrap: wrap;
            gap: 4px;
            max-width: 300px;
            max-height: 100px;
            overflow-y: auto;
            margin-bottom: 8px;
        }
        .timeline-sprints button {
            max-width: 100%;
            overflow-wrap: anywhere;
            font-size: 11px;
        }
        .timeline-scrubber input[type="range"] {
            width: 100%;
            accent-color: ${THEME.accent.purple};
        }
        .timeline-info {
            display: flex;
            justify-content: space-between;
            font-size: 12px;
            color: ${THEME.fgMuted};
            margin-bottom: 8px;
        }
        .timeline-speed {
            display: flex;
            align-items: center;
            gap: 8px;
            font-size: 12px;
        }
        .timeline-speed label {
            color: ${THEME.fgMuted};
        }
        .timeline-speed select {
            background: ${THEME.bgTertiary};
            border: 1px solid ${THEME.fgMuted};
            color: ${THEME.fg};
            padding: 2px 4px;
            border-radius: 4px;
        }

        /* Node animations */
        @keyframes nodeAppear {
            from { transform: scale(0); opacity: 0; }
            to { transform: scale(1); opacity: 1; }
        }
        @keyframes nodeDisappear {
            from { transform: scale(1); opacity: 1; }
            to { transform: scale(0); opacity: 0; }
        }
    `;
}

/**
 * Setup event listeners for timeline controls
 */
function setupTimeTravelListeners() {
    const controls = timeTravelState.controlsEl;
    if (!controls) return;

    controls.querySelector('.timeline-close').addEventListener('click', stopTimeTravel);
    controls.querySelector('#tt-start').addEventListener('click', () => goToCommit(0));
    controls.querySelector('#tt-back').addEventListener('click', () => stepCommit(-1));
    controls.querySelector('#tt-play').addEventListener('click', togglePlay);
    controls.querySelector('#tt-forward').addEventListener('click', () => stepCommit(1));
    controls.querySelector('#tt-end').addEventListener('click', () => {
        if (timeTravelState.history) {
            goToCommit(timeTravelState.history.commits.length - 1);
        }
    });

    controls.querySelector('#tt-slider').addEventListener('input', (e) => {
        if (timeTravelState.history) {
            const idx = Number(e.target.value);
            goToCommit(idx);
        }
    });

    controls.querySelector('#tt-speed').addEventListener('change', (e) => {
        timeTravelState.speed = parseFloat(e.target.value);
    });
}

/**
 * Start time-travel mode
 */
export function startTimeTravel() {
    if (!timeTravelState.history) {
        console.warn('[TimeTravel] No history loaded');
        return;
    }
    if (timeTravelState.active) return;

    // Save original state
    const graphData = store.graph?.graphData() || { nodes: [], links: [] };
    timeTravelState.originalNodes = [...graphData.nodes];
    timeTravelState.originalLinks = [...graphData.links];
    timeTravelState.nodeStates.clear();
    timeTravelState.autoPauseRedraw = store.graph?.autoPauseRedraw() ?? true;
    timeTravelState.warmupTicks = store.graph?.warmupTicks() ?? 0;
    // Nodes already have positions. Re-running synchronous layout warmup on
    // every history step blocks both controls and transition painting. Keep
    // the normal per-frame simulation running instead.
    store.graph?.warmupTicks(0);

    // Initialize all nodes as hidden
    graphData.nodes.forEach(node => {
        timeTravelState.nodeStates.set(node.id, {
            visible: false,
            from: 0,
            startedAt: 0,
            duration: 0
        });
    });

    timeTravelState.active = true;
    timeTravelState.currentIdx = 0;

    // Show controls
    if (timeTravelState.controlsEl) {
        timeTravelState.controlsEl.classList.add('active');
    }

    // Go to start
    goToCommit(0);

    dispatchEvent('timeTravelStart', {});
}

/**
 * Discard timeline state before replacing its graph or history.
 */
function resetTimeTravel() {
    stopTimeTravel();
    timeTravelState.history = null;
    timeTravelState.currentIdx = 0;
    timeTravelState.originalNodes = [];
    timeTravelState.originalLinks = [];
    timeTravelState.nodeStates.clear();
    timeTravelState.controlsEl?.remove();
    timeTravelState.controlsEl = null;
}

/**
 * Stop time-travel mode and restore the current graph.
 */
export function stopTimeTravel() {
    stopTimelineTransition();
    if (timeTravelState.animationFrame !== null) {
        cancelAnimationFrame(timeTravelState.animationFrame);
        timeTravelState.animationFrame = null;
    }
    if (!timeTravelState.active) return;

    // Stop playing
    if (timeTravelState.playing) {
        togglePlay();
    }

    // Restore original nodes
    store.graph?.warmupTicks(timeTravelState.warmupTicks);
    if (timeTravelState.originalNodes.length > 0) {
        timeTravelState.originalNodes.forEach(n => {
            if (n._ttOrigStatus !== undefined) {
                n.status = n._ttOrigStatus;
                delete n._ttOrigStatus;
            }
        });
    }
    if (store.graph && timeTravelState.originalNodes.length > 0) {
        store.graph.graphData({
            nodes: timeTravelState.originalNodes,
            links: timeTravelState.originalLinks
        });
    }

    timeTravelState.active = false;
    timeTravelState.nodeStates.clear();

    // Hide controls
    if (timeTravelState.controlsEl) {
        timeTravelState.controlsEl.classList.remove('active');
    }

    dispatchEvent('timeTravelStop', {});
}

/**
 * Toggle time-travel mode on or off
 * @returns {boolean} True if time-travel is now active
 */
export function toggleTimeTravel() {
    if (!timeTravelState.history) {
        console.warn('[TimeTravel] No history loaded');
        return false;
    }
    if (timeTravelState.active) {
        stopTimeTravel();
    } else {
        startTimeTravel();
    }
    return timeTravelState.active;
}

/**
 * Go to a specific commit index
 * @param {number} idx - Commit index
 */
function goToCommit(idx) {
    if (!timeTravelState.history || !timeTravelState.active) return;

    const commits = timeTravelState.history.commits;
    idx = Math.max(0, Math.min(idx, commits.length - 1));
    timeTravelState.currentIdx = idx;

    // Calculate which nodes should be visible and their historical status at this point
    const visibleNodes = new Set();
    const historicalStatus = new Map();
    (timeTravelState.history.initial_beads || []).forEach(id => {
        visibleNodes.add(id);
        historicalStatus.set(id, 'open');
    });

    // Walk through history up to current index
    for (let i = 0; i <= idx; i++) {
        const commit = commits[i];

        // Add nodes from this commit (historically open when created)
        if (commit.beads_added) {
            commit.beads_added.forEach(id => {
                visibleNodes.add(id);
                historicalStatus.set(id, 'open');
            });
        }

        // Remove closed nodes (historically closed once closed event has occurred)
        if (commit.beads_closed) {
            commit.beads_closed.forEach(id => {
                visibleNodes.delete(id);
                historicalStatus.set(id, 'closed');
            });
        }

        // Restore reopened nodes back to active/open visibility
        if (commit.beads_reopened) {
            commit.beads_reopened.forEach(id => {
                visibleNodes.add(id);
                historicalStatus.set(id, 'open');
            });
        }

        // Record removal is distinct from completion; either hides the node.
        if (commit.beads_removed) {
            commit.beads_removed.forEach(id => {
                visibleNodes.delete(id);
                historicalStatus.delete(id);
            });
        }

        if (commit.bead_statuses && typeof commit.bead_statuses === 'object') {
            Object.entries(commit.bead_statuses).forEach(([id, status]) => {
                historicalStatus.set(id, status);
            });
        }
    }

    // Reflect historical status at commit idx in-place without breaking node references
    timeTravelState.originalNodes.forEach(n => {
        const histStatus = historicalStatus.get(n.id);
        if (histStatus !== undefined) {
            if (n._ttOrigStatus === undefined) {
                n._ttOrigStatus = n.status;
            }
            n.status = histStatus;
        } else if (n._ttOrigStatus !== undefined) {
            n.status = n._ttOrigStatus;
            delete n._ttOrigStatus;
        }
    });

    // Animate from the currently rendered state, including interrupted scrubs.
    stopTimelineTransition();
    const now = performance.now();
    const duration = window.matchMedia('(prefers-reduced-motion: reduce)').matches ? 0 : 250;
    const currentCommit = commits[idx];
    timeTravelState.nodeStates.forEach((state, nodeId) => {
        const shouldBeVisible = visibleNodes.has(nodeId);
        state.from = timelineOpacity(state, now);
        state.visible = shouldBeVisible;
        state.startedAt = now;
        state.duration = state.from === (shouldBeVisible ? 1 : 0) ? 0 : duration;
    });

    // Build visible links (both endpoints must be visible)
    const visibleLinksArr = timeTravelState.originalLinks.filter(link => {
        const sourceId = typeof link.source === 'object' ? link.source.id : link.source;
        const targetId = typeof link.target === 'object' ? link.target.id : link.target;
        return visibleNodes.has(sourceId) && visibleNodes.has(targetId);
    });

    // Update graph
    const visibleNodesArr = timeTravelState.originalNodes.filter(n => visibleNodes.has(n.id));
    if (store.graph) {
        store.graph.graphData({
            nodes: visibleNodesArr,
            links: visibleLinksArr
        });
        if (duration > 0) {
            store.graph.autoPauseRedraw(false);
            redrawTimelineTransition();
        }
    }

    // Update UI
    updateTimeTravelUI();

    dispatchEvent('timeTravelCommit', {
        idx,
        commit: currentCommit,
        visibleNodes: visibleNodes.size
    });
}

/**
 * Step forward or backward by one commit
 * @param {number} delta - Direction (+1 or -1)
 */
function stepCommit(delta) {
    if (!timeTravelState.history) return;
    goToCommit(timeTravelState.currentIdx + delta);
}

/**
 * Toggle play/pause
 */
function togglePlay() {
    if (!timeTravelState.history || !timeTravelState.active) return;

    timeTravelState.playing = !timeTravelState.playing;

    // Update play button
    const playBtn = timeTravelState.controlsEl?.querySelector('#tt-play');
    if (playBtn) {
        playBtn.textContent = timeTravelState.playing ? '⏸️' : '▶️';
    }

    if (timeTravelState.playing) {
        timeTravelState.lastFrameTime = Date.now();
        playAnimation();
    } else if (timeTravelState.animationFrame !== null) {
        cancelAnimationFrame(timeTravelState.animationFrame);
        timeTravelState.animationFrame = null;
    }

    dispatchEvent('timeTravelPlayState', { playing: timeTravelState.playing });
}

/**
 * Animation loop for playback
 */
function playAnimation() {
    if (!timeTravelState.playing) return;

    const now = Date.now();
    const delta = now - timeTravelState.lastFrameTime;
    const interval = 1000 / timeTravelState.speed; // ms per frame

    if (delta >= interval) {
        timeTravelState.lastFrameTime = now;

        if (timeTravelState.currentIdx < timeTravelState.history.commits.length - 1) {
            stepCommit(1);
        } else {
            // Reached end, stop playing
            togglePlay();
            return;
        }
    }

    timeTravelState.animationFrame = requestAnimationFrame(playAnimation);
}

/**
 * Update timeline UI elements
 */
function updateTimeTravelUI() {
    if (!timeTravelState.controlsEl || !timeTravelState.history) return;

    const commits = timeTravelState.history.commits;
    const idx = timeTravelState.currentIdx;
    const commit = commits[idx];

    // Update slider
    const slider = timeTravelState.controlsEl.querySelector('#tt-slider');
    if (slider) {
        slider.value = idx;
    }

    // Update date
    const dateEl = timeTravelState.controlsEl.querySelector('#tt-date');
    if (dateEl && commit) {
        const date = new Date(commit.date);
        dateEl.textContent = date.toLocaleDateString('en-US', {
            year: 'numeric',
            month: 'short',
            day: 'numeric'
        });
    }

    // Update position
    const posEl = timeTravelState.controlsEl.querySelector('#tt-position');
    if (posEl) {
        posEl.textContent = `${idx + 1} / ${commits.length}`;
    }
}

/**
 * Check if time-travel is active
 */
export function isTimeTravelActive() {
    return timeTravelState.active;
}

/**
 * Get time-travel state for external access
 */
export function getTimeTravelState() {
    return {
        active: timeTravelState.active,
        playing: timeTravelState.playing,
        currentIdx: timeTravelState.currentIdx,
        totalCommits: timeTravelState.history?.commits?.length || 0,
        speed: timeTravelState.speed
    };
}

/**
 * Transform robot-history format to timeline format for time-travel
 * @param {Object} robotHistory - Output from bv --robot-history
 * @returns {Object} Timeline format { commits: [...] }
 */
export function transformHistoryToTimeline(robotHistory) {
    if (!robotHistory || !robotHistory.histories) {
        console.warn('[TimeTravel] Invalid robot-history format');
        return null;
    }

    // Collect all events across all beads
    const allEvents = [];

    Object.values(robotHistory.histories).forEach(beadHistory => {
        if (!beadHistory.events) return;

        beadHistory.events.forEach(event => {
            allEvents.push({
                beadId: event.bead_id,
                eventType: event.event_type,
                timestamp: event.timestamp,
                commitSha: event.commit_sha,
                commitMessage: event.commit_message || ''
            });
        });
    });

    // Sort events deterministically by timestamp, lifecycle order, commit SHA, and bead ID
    const EVENT_TYPE_ORDER = { created: 0, reopened: 1, modified: 2, closed: 3 };
    allEvents.sort((a, b) => {
        const timeDiff = new Date(a.timestamp) - new Date(b.timestamp);
        if (timeDiff !== 0) return timeDiff;
        const typeDiff = (EVENT_TYPE_ORDER[a.eventType] ?? 99) - (EVENT_TYPE_ORDER[b.eventType] ?? 99);
        if (typeDiff !== 0) return typeDiff;
        const shaDiff = String(a.commitSha || '').localeCompare(String(b.commitSha || ''));
        if (shaDiff !== 0) return shaDiff;
        return String(a.beadId || '').localeCompare(String(b.beadId || ''));
    });

    // Group events by commit SHA
    const commitMap = new Map();
    allEvents.forEach(event => {
        const sha = event.commitSha;
        if (!commitMap.has(sha)) {
            commitMap.set(sha, {
                sha: sha,
                date: event.timestamp,
                message: event.commitMessage,
                beads_added: [],
                beads_closed: [],
                beads_reopened: [],
                beads_modified: []
            });
        }

        const commit = commitMap.get(sha);
        if (event.eventType === 'created') {
            commit.beads_added.push(event.beadId);
        } else if (event.eventType === 'closed') {
            commit.beads_closed.push(event.beadId);
        } else if (event.eventType === 'reopened') {
            commit.beads_reopened.push(event.beadId);
        } else if (event.eventType === 'modified') {
            commit.beads_modified.push(event.beadId);
        }
    });

    // Convert to array sorted deterministically by date, then commit SHA
    const commits = [...commitMap.values()].sort((a, b) => {
        const timeDiff = new Date(a.date) - new Date(b.date);
        if (timeDiff !== 0) return timeDiff;
        return String(a.sha || '').localeCompare(String(b.sha || ''));
    });

    // Compute cumulative historical status per bead at each commit index
    const runningStatuses = {};
    commits.forEach(commit => {
        commit.beads_added.forEach(id => { runningStatuses[id] = 'open'; });
        commit.beads_reopened.forEach(id => { runningStatuses[id] = 'open'; });
        commit.beads_closed.forEach(id => { runningStatuses[id] = 'closed'; });
        commit.bead_statuses = { ...runningStatuses };
    });

    return { commits };
}

/**
 * Generate demo history data for testing time-travel
 * @param {Array} issues - Array of issue objects
 * @param {number} numCommits - Number of commits to simulate
 * @returns {Object} Timeline format { commits: [...] }
 */
export function generateDemoHistory(issues, numCommits = 20) {
    if (!issues || issues.length === 0) return null;

    const commits = [];
    const issueIds = issues.map(i => i.id);
    const addedSet = new Set();
    const closedSet = new Set();

    // Start date: 30 days ago
    const startDate = new Date();
    startDate.setDate(startDate.getDate() - 30);

    // Issues to add per commit (distribute evenly)
    const issuesPerCommit = Math.ceil(issueIds.length / numCommits);

    for (let i = 0; i < numCommits; i++) {
        const commitDate = new Date(startDate);
        commitDate.setDate(startDate.getDate() + (i * 30 / numCommits));

        const commit = {
            sha: `demo${i.toString().padStart(4, '0')}`,
            date: commitDate.toISOString(),
            message: `Demo commit ${i + 1}`,
            beads_added: [],
            beads_closed: []
        };

        // Add some issues
        for (let j = 0; j < issuesPerCommit && addedSet.size < issueIds.length; j++) {
            const idx = addedSet.size;
            if (idx < issueIds.length) {
                const issueId = issueIds[idx];
                commit.beads_added.push(issueId);
                addedSet.add(issueId);
            }
        }

        // Close some previously added issues (after first few commits)
        if (i > 3 && i % 3 === 0) {
            const addedArr = [...addedSet].filter(id => !closedSet.has(id));
            const toClose = addedArr.slice(0, Math.floor(addedArr.length * 0.2));
            toClose.forEach(id => {
                const issue = issues.find(iss => iss.id === id);
                if (issue && isResolvedIssue(issue)) {
                    commit.beads_closed.push(id);
                    closedSet.add(id);
                }
            });
        }

        commits.push(commit);
    }

    return { commits };
}
