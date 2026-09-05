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

const THEME = {
    // Dracula-inspired palette
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
        this.wasmReady = false;
        this.container = null;

        // Data
        this.issues = [];
        this.dependencies = [];
        this.nodeMap = new Map();      // id -> issue
        this.nodeIndexMap = new Map(); // id -> index

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

        // Heatmap & metrics mode
        this.heatmapMode = false;
        this.sizeMetric = 'pagerank'; // pagerank | betweenness | critical | indegree
        this.maxMetrics = { pagerank: 1, betweenness: 1, critical: 1, indegree: 1 };

        // Filters
        this.filters = {
            status: null,
            priority: null,
            labels: [],
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
        this.selectedNode = null;
        this.hoveredNode = null;
        this.highlightedNodes.clear();
        this.highlightedLinks.clear();
        this.connectedNodes.clear();
        this.focusedPath = null;
        this.heatmapMode = false;
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

/**
 * Compute max metric values for normalization
 * Note: Must be called AFTER prepareGraphData since metrics are on prepared nodes
 */
function computeMaxMetrics() {
    // Get prepared nodes from graph data if available, otherwise use raw issues
    const graphData = store.graph?.graphData();
    const nodes = graphData?.nodes || store.issues;
    if (!nodes.length) return;

    store.maxMetrics.pagerank = Math.max(...nodes.map(n => n.pagerank || 0), 0.001);
    store.maxMetrics.betweenness = Math.max(...nodes.map(n => n.betweenness || 0), 0.001);
    store.maxMetrics.critical = Math.max(...nodes.map(n => n.criticalDepth || 0), 1);
    store.maxMetrics.indegree = Math.max(...nodes.map(n => n.blockerCount || 0), 1);
}

/**
 * Get heatmap color for a node based on current sizeMetric
 * Uses HSL: 120 (green) to 0 (red) based on metric intensity
 */
function getHeatmapColor(node) {
    let val = 0, max = 1;
    switch (store.sizeMetric) {
        case 'pagerank':
            val = node.pagerank || 0;
            max = store.maxMetrics.pagerank;
            break;
        case 'betweenness':
            val = node.betweenness || 0;
            max = store.maxMetrics.betweenness;
            break;
        case 'critical':
            val = node.criticalDepth || 0;
            max = store.maxMetrics.critical;
            break;
        case 'indegree':
            val = node.blockerCount || 0;
            max = store.maxMetrics.indegree;
            break;
    }
    const ratio = Math.min(val / max, 1);
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
    if (['pagerank', 'betweenness', 'critical', 'indegree'].includes(metric)) {
        store.sizeMetric = metric;
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

function buildWasmGraph() {
    if (!store.wasmReady) return;

    try {
        const { DiGraph } = window.bvGraphWasm;

        if (store.wasmGraph) {
            store.wasmGraph.free();
            store.wasmGraph = null;
        }

        store.wasmGraph = DiGraph.withCapacity(store.issues.length, store.dependencies.length);

        // Add all nodes
        store.issues.forEach(issue => {
            store.wasmGraph.addNode(issue.id);
        });

        // Add blocking edges
        store.dependencies
            .filter(d => d.type === 'blocks' || !d.type)
            .forEach(d => {
                const fromIdx = store.wasmGraph.nodeIdx(d.issue_id);
                const toIdx = store.wasmGraph.nodeIdx(d.depends_on_id);
                if (fromIdx !== undefined && toIdx !== undefined) {
                    store.wasmGraph.addEdge(fromIdx, toIdx);
                }
            });

        console.log(`[bv-graph] WASM graph: ${store.wasmGraph.nodeCount()} nodes, ${store.wasmGraph.edgeCount()} edges`);
    } catch (e) {
        console.warn('[bv-graph] Failed to build WASM graph:', e);
        store.wasmGraph = null;
    }
}

// Keep the viewer's metric names separate from wasm-bindgen's serialized fields.
export function computeHITSMetrics(wasmGraph) {
    const result = wasmGraph.hitsDefault();
    return { hub: result.hubs, authority: result.authorities };
}

function computeMetrics() {
    if (!store.wasmReady || !store.wasmGraph) return;

    const start = performance.now();

    try {
        // PageRank (importance)
        store.metrics.pagerank = store.wasmGraph.pagerankDefault();

        // Critical path heights (depth)
        store.metrics.criticalPath = store.wasmGraph.criticalPathHeights();

        // Eigenvector (influence)
        store.metrics.eigenvector = store.wasmGraph.eigenvectorDefault();

        // K-core (cohesion)
        store.metrics.kcore = store.wasmGraph.kcore();

        // Betweenness (bottleneck) - use approx for large graphs
        const nodeCount = store.wasmGraph.nodeCount();
        if (nodeCount > 500) {
            store.metrics.betweenness = store.wasmGraph.betweennessApprox(Math.min(100, nodeCount));
        } else if (nodeCount > 0) {
            store.metrics.betweenness = store.wasmGraph.betweenness();
        }

        // HITS (hub and authority scores)
        try {
            store.metrics.hits = computeHITSMetrics(store.wasmGraph);
        } catch (e) {
            console.warn('[bv-graph] HITS computation skipped:', e);
        }

        // Slack (schedule flexibility - 0 means on critical path)
        try {
            store.metrics.slack = store.wasmGraph.slack();
        } catch (e) {
            console.warn('[bv-graph] Slack computation skipped:', e);
        }

        // Articulation points (cut vertices - removing them disconnects the graph)
        try {
            const artPoints = store.wasmGraph.articulationPoints();
            if (artPoints && artPoints.length > 0) {
                // Convert indices to node IDs
                store.metrics.articulationPoints = new Set(
                    Array.from(artPoints).map(idx => {
                        for (const [id, i] of store.nodeIndexMap.entries()) {
                            if (i === idx) return id;
                        }
                        return null;
                    }).filter(Boolean)
                );
            } else {
                store.metrics.articulationPoints = new Set();
            }
        } catch (e) {
            console.warn('[bv-graph] Articulation points computation skipped:', e);
        }

        // Cycles
        const cycleResult = store.wasmGraph.enumerateCycles(100);
        store.metrics.cycles = cycleResult;

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

    // Clear container
    store.container.innerHTML = '';

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
        .linkDirectionalParticles(node => store.config.enableParticles ? 2 : 0)
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
        });

    // Setup keyboard shortcuts
    setupKeyboardShortcuts();

    // Emit ready event
    dispatchEvent('ready', { graph: store.graph, wasmReady: store.wasmReady });

    return store.graph;
}

// ============================================================================
// DATA LOADING
// ============================================================================

// Pre-computed layout cache
let precomputedLayout = null;

/**
 * Load pre-computed graph layout for instant rendering.
 * This fetches the compact layout file (~30KB) which contains positions and metrics.
 * @returns {Promise<object|null>} Layout data or null if not available
 */
export async function loadPrecomputedLayout() {
    try {
        const response = await fetch('data/graph_layout.json');
        if (!response.ok) return null;
        precomputedLayout = await response.json();
        console.log(`[bv-graph] Pre-computed layout: ${precomputedLayout.node_count} nodes`);
        return precomputedLayout;
    } catch (e) {
        console.log('[bv-graph] No pre-computed layout, will use dynamic simulation');
        return null;
    }
}

/**
 * Load data with optional pre-computed layout for instant rendering.
 * @param {Array} issues - Issue objects from SQLite
 * @param {Array} dependencies - Dependency objects from SQLite
 * @param {object} [layout] - Optional pre-computed layout
 */
export function loadData(issues, dependencies, layout = precomputedLayout) {
    store.reset();
    store.issues = issues;
    store.dependencies = dependencies;

    // Build lookup maps
    issues.forEach((issue, idx) => {
        store.nodeMap.set(issue.id, issue);
        store.nodeIndexMap.set(issue.id, idx);
    });

    // Merge pre-computed metrics if available
    if (layout?.metrics) {
        issues.forEach(issue => {
            const m = layout.metrics[issue.id];
            if (m) {
                issue._precomputed = {
                    pagerank: m[0],
                    betweenness: m[1],
                    inDegree: m[2],
                    outDegree: m[3],
                    inCycle: m[4] === 1
                };
            }
        });
    }

    // Build WASM graph structure (always, for cycle navigator etc.)
    // Only skip metric computation if we have pre-computed metrics
    if (store.wasmReady) {
        buildWasmGraph();
        if (!layout?.metrics) {
            computeMetrics();
        } else {
            console.log('[bv-graph] Using pre-computed metrics, skipping WASM computation');
            // Convert pre-computed cycle IDs to WASM indices for cycle navigator compatibility
            if (layout.cycles && store.wasmGraph) {
                const cyclesAsIndices = layout.cycles.map(cycle =>
                    cycle.map(id => store.wasmGraph.nodeIdx(id)).filter(idx => idx !== undefined)
                ).filter(cycle => cycle.length > 0);
                store.metrics.cycles = { cycles: cyclesAsIndices, count: cyclesAsIndices.length };
            }
        }
    }

    // Build label color map for galaxy view
    buildLabelColorMap();

    // Prepare graph data with optional pre-computed positions
    const graphData = prepareGraphData(layout);

    // Update graph
    store.graph.graphData(graphData);

    // Compute max metric values for heatmap normalization (after graph data is set)
    computeMaxMetrics();

    // Fit immediately if pre-computed, otherwise wait for simulation
    if (layout?.positions) {
        store.graph.zoomToFit(200, 50);
    } else {
        setTimeout(() => store.graph.zoomToFit(400, 50), 500);
    }

    // Emit event
    dispatchEvent('dataLoaded', {
        nodeCount: graphData.nodes.length,
        linkCount: graphData.links.length,
        metrics: store.metrics,
        precomputed: !!layout
    });

    return graphData;
}

function prepareGraphData(layout = null) {
    const { issues, dependencies, filters, metrics } = store;

    // Filter nodes
    let nodes = issues.filter(issue => {
        // Status filter
        if (filters.status && issue.status !== filters.status) return false;
        if (!filters.showClosed && issue.status === 'closed') return false;

        // Priority filter
        if (filters.priority !== null && issue.priority !== filters.priority) return false;

        // Label filter
        if (filters.labels.length > 0) {
            const issueLabels = issue.labels || [];
            if (!filters.labels.some(l => issueLabels.includes(l))) return false;
        }

        // Search filter
        if (filters.search) {
            const term = filters.search.toLowerCase();
            const searchable = `${issue.id} ${issue.title} ${issue.description || ''}`.toLowerCase();
            if (!searchable.includes(term)) return false;
        }

        return true;
    });

    // Build node set for link filtering
    const nodeIds = new Set(nodes.map(n => n.id));

    // Filter links
    let links = dependencies
        .filter(d => (d.type === 'blocks' || !d.type))
        .filter(d => nodeIds.has(d.issue_id) && nodeIds.has(d.depends_on_id))
        .map(d => ({
            source: d.issue_id,
            target: d.depends_on_id,
            type: d.type || 'blocks'
        }));

    // Enrich nodes with computed data
    nodes = nodes.map(issue => {
        const idx = store.wasmReady ? store.wasmGraph?.nodeIdx(issue.id) : undefined;
        const pre = issue._precomputed; // Pre-computed metrics from layout
        const pos = layout?.positions?.[issue.id]; // Pre-computed position

        return {
            id: issue.id,
            title: issue.title,
            description: issue.description,
            status: issue.status || 'open',
            priority: issue.priority ?? 2,
            type: issue.type || 'task',
            labels: issue.labels || [],
            assignee: issue.assignee,
            createdAt: issue.created_at,
            updatedAt: issue.updated_at,

            // Computed metrics (prefer pre-computed)
            pagerank: pre?.pagerank ?? (idx !== undefined && metrics.pagerank ? metrics.pagerank[idx] : 0),
            betweenness: pre?.betweenness ?? (idx !== undefined && metrics.betweenness ? metrics.betweenness[idx] : 0),
            criticalDepth: idx !== undefined && metrics.criticalPath ? metrics.criticalPath[idx] : 0,
            eigenvector: idx !== undefined && metrics.eigenvector ? metrics.eigenvector[idx] : 0,
            kcore: idx !== undefined && metrics.kcore ? metrics.kcore[idx] : 0,
            inCycle: pre?.inCycle ?? false,

            // Dependency counts. Prefer the active-blocker count from the
            // materialized view (issue.blocker_count / dependent_count) so
            // closed blockers don't keep their dependents looking blocked
            // (bv-issue#143/#144). Fall back to pre-computed graph degrees,
            // then to a raw dependency-row count.
            blockerCount: issue.blocker_count
                ?? pre?.inDegree
                ?? dependencies.filter(d => d.issue_id === issue.id).length,
            dependentCount: issue.dependent_count
                ?? pre?.outDegree
                ?? dependencies.filter(d => d.depends_on_id === issue.id).length,

            // Position: pre-computed uses fx/fy to skip simulation
            x: pos ? pos[0] : undefined,
            y: pos ? pos[1] : undefined,
            fx: pos ? pos[0] : null,
            fy: pos ? pos[1] : null
        };
    });

    // Mark cycle nodes (if not from pre-computed)
    if (!layout?.cycles && metrics.cycles?.cycles) {
        const cycleNodes = new Set(metrics.cycles.cycles.flat());
        nodes.forEach(node => {
            const idx = store.wasmGraph?.nodeIdx(node.id);
            node.inCycle = idx !== undefined && cycleNodes.has(idx);
        });
    }

    return { nodes, links };
}

// ============================================================================
// NODE RENDERING
// ============================================================================

function getNodeSize(node) {
    const { nodeMinSize, nodeMaxSize } = store.config;

    // Use PageRank for sizing (normalized 0-1)
    let score = node.pagerank || 0;

    // Boost for high betweenness (bottleneck nodes)
    if (node.betweenness > 0.1) {
        score = Math.min(1, score + 0.2);
    }

    return nodeMinSize + score * (nodeMaxSize - nodeMinSize);
}

function getNodeColor(node) {
    // What-if simulation states take priority
    if (node._whatIfState === 'closing') return THEME.accent.green;
    if (node._whatIfState === 'unblocked') return THEME.accent.cyan;

    // Critical path nodes get red/orange highlight
    if (node._criticalPathState === 'active') return THEME.accent.red;

    // Cycle navigator highlight (current cycle)
    if (node._cycleHighlight) return THEME.accent.red;

    // Cycle nodes get special color (all cycles, dimmer)
    if (node.inCycle) return THEME.accent.pink;

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
    // active (non-closed, non-tombstoned) blocker is treated as blocked.
    // The materialized view already excludes resolved blockers from
    // blockerCount, so this branch only fires when there really is an
    // open dependency upstream (bv-issue#143/#144).
    if (
        node.blockerCount > 0
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
    if (node.status === 'closed') return 0.6;

    return 1;
}

function drawNode(node, ctx, globalScale) {
    const size = getNodeSize(node);
    const color = getNodeColor(node);
    const opacity = getNodeOpacity(node);
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
    const linkId = `${link.source?.id || link.source}-${link.target?.id || link.target}`;
    const sourceNode = typeof link.source === 'object' ? link.source : store.nodeMap.get(link.source);
    const targetNode = typeof link.target === 'object' ? link.target : store.nodeMap.get(link.target);
    const srcId = sourceNode?.id || link.source;
    const tgtId = targetNode?.id || link.target;

    // What-if cascade links (bright green for unblocking edges)
    if (whatIfState.active && store.highlightedLinks.has(linkId)) {
        if (sourceNode?._whatIfState === 'closing' || targetNode?._whatIfState === 'unblocked') {
            return THEME.accent.green;
        }
    }

    // Critical path links (red)
    if (criticalPathState.active && store.highlightedLinks.has(linkId)) {
        if (sourceNode?._criticalPathState || targetNode?._criticalPathState) {
            return THEME.link.critical;
        }
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

    // Heatmap mode: color links by slack/urgency of connected nodes
    // Low slack = urgent (orange/red), high slack = relaxed (cyan/blue)
    // Only apply if at least one node has real slack data (not undefined/Infinity)
    if (store.heatmapMode && sourceNode && targetNode) {
        const srcSlack = sourceNode.slack;
        const tgtSlack = targetNode.slack;
        // Only color by slack if at least one node has real slack data
        const srcHasSlack = srcSlack !== undefined && srcSlack !== null && srcSlack !== Infinity;
        const tgtHasSlack = tgtSlack !== undefined && tgtSlack !== null && tgtSlack !== Infinity;

        if (srcHasSlack || tgtHasSlack) {
            // Use minimum slack of nodes that have data
            const minSlack = Math.min(
                srcHasSlack ? srcSlack : Infinity,
                tgtHasSlack ? tgtSlack : Infinity
            );

            // Normalize slack (0-14 days typical range, cap at 14)
            const normalizedSlack = Math.min(minSlack, 14) / 14;

            // Color gradient: red (urgent) -> orange -> yellow -> cyan (relaxed)
            if (normalizedSlack < 0.25) {
                return THEME.priority.p0 + 'aa'; // Red with alpha (very urgent)
            } else if (normalizedSlack < 0.5) {
                return THEME.priority.p1 + 'aa'; // Orange (moderately urgent)
            } else if (normalizedSlack < 0.75) {
                return THEME.accent.yellow + 'aa'; // Yellow (some slack)
            } else {
                return THEME.accent.cyan + '88'; // Cyan (plenty of slack)
            }
        }
        // If neither node has slack data, fall through to default color
    }

    return THEME.link.default;
}

function getLinkOpacity(link) {
    if (store.highlightedLinks.size > 0) {
        const linkId = `${link.source?.id || link.source}-${link.target?.id || link.target}`;
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
    const linkId = `${srcId}-${tgtId}`;

    // Highlighted links (what-if, critical path, etc.) get thick width
    if (store.highlightedLinks.has(linkId)) {
        return Math.max(3, 3 / globalScale);
    }

    // Gold connected subgraph links get medium-thick width
    if (store.connectedNodes.size > 0 && store.connectedNodes.has(srcId) && store.connectedNodes.has(tgtId)) {
        return Math.max(2.5, 3 / globalScale);
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

    ctx.save();
    ctx.globalAlpha = opacity;
    ctx.strokeStyle = color;
    ctx.lineWidth = width;

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

    // Shift+click: what-if simulation
    // Ctrl+click: show dependencies
    // Regular click: select

    if (event.shiftKey) {
        performWhatIf(node);
    } else if (event.ctrlKey || event.metaKey) {
        highlightDependencyPath(node);
    } else {
        // If what-if is active, reset it
        if (whatIfState.active) {
            resetWhatIf();
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
    unblockedNodes: new Set(),
    animationPhase: 0,
    animationTimer: null
};

/**
 * Perform what-if simulation for closing an issue
 * @param {Object} node - The node to simulate closing
 */
export function performWhatIf(node) {
    if (!node || !store.wasmReady || !store.wasmGraph) {
        console.warn('[bv-graph] What-if: WASM not ready');
        return null;
    }

    // Only simulate on open issues
    if (node.status === 'closed') {
        showToast('Issue is already closed', 'info');
        return null;
    }

    const idx = store.wasmGraph.nodeIdx(node.id);
    if (idx === undefined) return null;

    // Build closed set from current issue states
    const closedSet = buildClosedSet();

    // Call WASM what-if
    let result;
    try {
        result = store.wasmGraph.whatIfClose(idx, closedSet);
        if (typeof result === 'string') {
            result = JSON.parse(result);
        }
    } catch (e) {
        console.error('[bv-graph] What-if computation failed:', e);
        return null;
    }

    // Animate the cascade
    animateWhatIfCascade(node, result);

    return result;
}

/**
 * Build a boolean array indicating which nodes are already closed
 */
function buildClosedSet() {
    const n = store.wasmGraph.nodeCount();
    const closedSet = new Uint8Array(n);

    store.issues.forEach(issue => {
        if (issue.status === 'closed') {
            const idx = store.wasmGraph.nodeIdx(issue.id);
            if (idx !== undefined && idx < n) {
                closedSet[idx] = 1;
            }
        }
    });

    return closedSet;
}

/**
 * Animate the what-if cascade effect
 */
function animateWhatIfCascade(sourceNode, result) {
    // Clear any existing animation
    resetWhatIf();

    whatIfState.active = true;
    whatIfState.sourceNode = sourceNode;
    whatIfState.unblockedNodes.clear();

    // Get unblocked node IDs
    const unblockedIds = (result.unblocked_ids || []).map(idx => {
        return store.wasmGraph.nodeId(idx);
    }).filter(Boolean);

    // Phase 1: Highlight the source node (pulse green, "closing")
    store.highlightedNodes.clear();
    store.highlightedLinks.clear();
    store.highlightedNodes.add(sourceNode.id);

    // Add closing visual state to source
    const graphData = store.graph.graphData();
    const sourceGraphNode = graphData.nodes.find(n => n.id === sourceNode.id);
    if (sourceGraphNode) {
        sourceGraphNode._whatIfState = 'closing';
    }

    refreshGraph();
    dispatchEvent('whatIfStart', { node: sourceNode, result });

    // Phase 2: Ripple out to unblocked nodes with staggered animation
    whatIfState.animationPhase = 1;
    let delay = 300;

    unblockedIds.forEach((id, i) => {
        setTimeout(() => {
            whatIfState.unblockedNodes.add(id);
            store.highlightedNodes.add(id);

            // Mark node as unblocked
            const unblockedNode = graphData.nodes.find(n => n.id === id);
            if (unblockedNode) {
                unblockedNode._whatIfState = 'unblocked';
            }

            // Highlight the edge from blocker to this node
            store.dependencies.forEach(dep => {
                if (dep.issue_id === sourceNode.id && dep.depends_on_id === id) {
                    store.highlightedLinks.add(`${sourceNode.id}-${id}`);
                }
                // Also highlight edges from other closed nodes that contribute
                const blocker = store.nodeMap.get(dep.issue_id);
                if (blocker && (blocker.status === 'closed' || dep.issue_id === sourceNode.id) && dep.depends_on_id === id) {
                    store.highlightedLinks.add(`${dep.issue_id}-${id}`);
                }
            });

            refreshGraph();
            dispatchEvent('whatIfUnblock', { nodeId: id, index: i, total: unblockedIds.length });
        }, delay + i * 150);
    });

    // Phase 3: Show summary after animations complete
    const summaryDelay = delay + unblockedIds.length * 150 + 200;
    whatIfState.animationTimer = setTimeout(() => {
        whatIfState.animationPhase = 2;
        showWhatIfSummary(sourceNode, result, unblockedIds);
    }, summaryDelay);
}

/**
 * Show what-if summary popup
 */
function showWhatIfSummary(sourceNode, result, unblockedIds) {
    const directCount = result.direct_unblocks || unblockedIds.length;
    const transitiveCount = result.transitive_unblocks || directCount;
    const parallelGain = result.parallel_gain || 0;

    dispatchEvent('whatIfComplete', {
        node: sourceNode,
        directUnblocks: directCount,
        transitiveUnblocks: transitiveCount,
        parallelGain: parallelGain,
        unblockedIds: unblockedIds
    });

    // Create summary toast
    if (directCount > 0) {
        showToast(
            `Closing ${sourceNode.id} would unblock ${directCount} issue${directCount > 1 ? 's' : ''} directly` +
            (transitiveCount > directCount ? `, ${transitiveCount} total in cascade` : ''),
            'success'
        );
    } else {
        showToast(`Closing ${sourceNode.id} would not immediately unblock any issues`, 'info');
    }
}

/**
 * Reset what-if visualization state
 */
export function resetWhatIf() {
    if (whatIfState.animationTimer) {
        clearTimeout(whatIfState.animationTimer);
        whatIfState.animationTimer = null;
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
    whatIfState.unblockedNodes.clear();
    whatIfState.animationPhase = 0;

    store.highlightedNodes.clear();
    store.highlightedLinks.clear();
    store.graph?.refresh();

    dispatchEvent('whatIfReset');
}

/**
 * Check if what-if simulation is active
 */
export function isWhatIfActive() {
    return whatIfState.active;
}

/**
 * Get what-if state
 */
export function getWhatIfState() {
    return {
        active: whatIfState.active,
        sourceNode: whatIfState.sourceNode,
        unblockedCount: whatIfState.unblockedNodes.size
    };
}

// ============================================================================
// CRITICAL PATH ANIMATION
// ============================================================================

// Critical path animation state
const criticalPathState = {
    active: false,
    path: [],           // Node IDs in path order
    pathIndices: [],    // Node indices in path order
    currentStep: 0,
    animationTimer: null,
    pathLength: 0
};

/**
 * Animate the critical path traversal
 * @param {boolean} animate - Whether to animate or just highlight
 */
export function animateCriticalPath(animate = true) {
    if (!store.wasmReady || !store.wasmGraph) {
        console.warn('[bv-graph] Critical path: WASM not ready');
        return null;
    }

    // Reset any existing animation
    resetCriticalPath();

    // Get the longest critical path using kCriticalPaths(1)
    let pathResult;
    try {
        pathResult = store.wasmGraph.kCriticalPathsDefault();
        if (typeof pathResult === 'string') {
            pathResult = JSON.parse(pathResult);
        }
    } catch (e) {
        console.error('[bv-graph] Critical path computation failed:', e);
        return null;
    }

    if (!pathResult?.paths?.length || !pathResult.paths[0]?.nodes?.length) {
        showToast('No critical path found (graph may be empty or cyclic)', 'info');
        return null;
    }

    const longestPath = pathResult.paths[0];
    criticalPathState.pathIndices = longestPath.nodes;
    criticalPathState.pathLength = longestPath.length;

    // Convert indices to node IDs
    criticalPathState.path = longestPath.nodes.map(idx =>
        store.wasmGraph.nodeId(idx)
    ).filter(Boolean);

    if (criticalPathState.path.length === 0) {
        showToast('Critical path nodes not found in graph', 'warning');
        return null;
    }

    criticalPathState.active = true;
    store.highlightedNodes.clear();
    store.highlightedLinks.clear();

    dispatchEvent('criticalPathStart', {
        pathLength: criticalPathState.pathLength,
        nodeCount: criticalPathState.path.length
    });

    if (animate) {
        // Animate traversal from source to sink
        animateCriticalPathTraversal();
    } else {
        // Just highlight all at once
        criticalPathState.path.forEach(id => store.highlightedNodes.add(id));
        highlightCriticalPathLinks();
        refreshGraph();
        showCriticalPathSummary();
    }

    return {
        path: criticalPathState.path,
        length: criticalPathState.pathLength
    };
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
        const graphNode = graphData.nodes.find(n => n.id === nodeId);
        if (graphNode) {
            graphNode._criticalPathState = 'active';
        }

        // Highlight the edge from previous node
        if (criticalPathState.currentStep > 0) {
            const prevNodeId = path[criticalPathState.currentStep - 1];
            store.highlightedLinks.add(`${prevNodeId}-${nodeId}`);
            store.highlightedLinks.add(`${nodeId}-${prevNodeId}`); // Both directions
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
        store.highlightedLinks.add(`${path[i]}-${path[i+1]}`);
        store.highlightedLinks.add(`${path[i+1]}-${path[i]}`);
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

    showToast(
        `Critical path: ${path.length} nodes, from ${sourceId} to ${sinkId}`,
        'info'
    );

    dispatchEvent('criticalPathComplete', {
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

    // Clear visual states
    const graphData = store.graph?.graphData();
    if (graphData) {
        graphData.nodes.forEach(node => {
            delete node._criticalPathState;
        });
    }

    criticalPathState.active = false;
    criticalPathState.path = [];
    criticalPathState.pathIndices = [];
    criticalPathState.currentStep = 0;
    criticalPathState.pathLength = 0;

    store.highlightedNodes.clear();
    store.highlightedLinks.clear();
    store.graph?.refresh();

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
        path: [...criticalPathState.path],
        length: criticalPathState.pathLength,
        currentStep: criticalPathState.currentStep
    };
}

// ============================================================================
// SELECTION & HIGHLIGHTING
// ============================================================================

export function selectNode(node) {
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

    if (!node || !store.wasmReady) return;

    const idx = store.wasmGraph.nodeIdx(node.id);
    if (idx === undefined) return;

    // Get all nodes that block this one (upstream)
    const blockers = store.wasmGraph.reachableTo(idx);
    // Get all nodes blocked by this one (downstream)
    const dependents = store.wasmGraph.reachableFrom(idx);

    // Highlight nodes
    store.highlightedNodes.add(node.id);
    [...blockers, ...dependents].forEach(i => {
        const id = store.wasmGraph.nodeId(i);
        if (id) store.highlightedNodes.add(id);
    });

    // Highlight links
    const graphData = store.graph.graphData();
    graphData.links.forEach(link => {
        const sourceId = link.source?.id || link.source;
        const targetId = link.target?.id || link.target;
        if (store.highlightedNodes.has(sourceId) && store.highlightedNodes.has(targetId)) {
            store.highlightedLinks.add(`${sourceId}-${targetId}`);
        }
    });

    store.focusedPath = { center: node.id, blockers, dependents };
    refreshGraph();

    dispatchEvent('pathHighlight', { node, blockerCount: blockers.length, dependentCount: dependents.length });
}

export function highlightCriticalPath() {
    if (!store.wasmReady || !store.wasmGraph) return;

    const criticalNodes = store.wasmGraph.criticalPathNodes();
    store.highlightedNodes = new Set(
        criticalNodes.map(idx => store.wasmGraph.nodeId(idx)).filter(Boolean)
    );
    refreshGraph();

    dispatchEvent('criticalPathHighlight', { nodeCount: criticalNodes.length });
}

export function highlightCycles() {
    if (!store.metrics.cycles?.cycles) return;

    const cycleNodeIndices = new Set(store.metrics.cycles.cycles.flat());
    store.highlightedNodes = new Set(
        [...cycleNodeIndices].map(idx => store.wasmGraph?.nodeId(idx)).filter(Boolean)
    );
    refreshGraph();

    dispatchEvent('cycleHighlight', { cycleCount: store.metrics.cycles.count });
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
    highlightedCycleEdges: new Set()
};

/**
 * Initialize cycle navigator with detected cycles
 */
export function initCycleNavigator() {
    if (!store.metrics.cycles?.cycles?.length) {
        cycleNavigatorState.active = false;
        cycleNavigatorState.cycles = [];
        cycleNavigatorState.currentIndex = 0;
        dispatchEvent('cycleNavigatorInit', { hasCycles: false, cycleCount: 0 });
        return null;
    }

    // Convert cycle indices to node IDs
    cycleNavigatorState.cycles = store.metrics.cycles.cycles.map(cycle =>
        cycle.map(idx => store.wasmGraph?.nodeId(idx)).filter(Boolean)
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

    // Highlight cycle edges (including wraparound edge)
    cycleNavigatorState.highlightedCycleEdges.clear();
    for (let i = 0; i < cycle.length; i++) {
        const from = cycle[i];
        const to = cycle[(i + 1) % cycle.length];
        const edgeId = `${from}-${to}`;
        store.highlightedLinks.add(edgeId);
        cycleNavigatorState.highlightedCycleEdges.add(edgeId);
    }

    // Mark nodes with cycle state for enhanced visual
    const graphData = store.graph?.graphData();
    if (graphData) {
        graphData.nodes.forEach(node => {
            node._cycleHighlight = cycleNavigatorState.highlightedCycleNodes.has(node.id);
        });
    }

    store.graph?.refresh();

    // Zoom to cycle if requested
    if (zoom) {
        zoomToCycle(index);
    }

    dispatchEvent('cycleHighlightChange', {
        currentIndex: index,
        cycleCount: cycleNavigatorState.cycles.length,
        cycle: cycle,
        cyclePath: formatCyclePath(cycle)
    });

    return {
        index,
        cycle,
        path: formatCyclePath(cycle)
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
    cycleNavigatorState.active = false;
    cycleNavigatorState.currentIndex = 0;
    cycleNavigatorState.highlightedCycleNodes.clear();
    cycleNavigatorState.highlightedCycleEdges.clear();

    // Clear cycle highlight state from nodes
    const graphData = store.graph?.graphData();
    if (graphData) {
        graphData.nodes.forEach(node => {
            delete node._cycleHighlight;
        });
    }

    store.highlightedNodes.clear();
    store.highlightedLinks.clear();
    store.graph?.refresh();

    dispatchEvent('cycleNavigatorReset');
}

/**
 * Toggle cycle navigator (highlight all cycles or reset)
 */
export function toggleCycleNavigator() {
    if (cycleNavigatorState.active && cycleNavigatorState.highlightedCycleNodes.size > 0) {
        resetCycleNavigator();
    } else {
        initCycleNavigator();
        if (cycleNavigatorState.cycles.length > 0) {
            highlightCycle(0, true);
        } else {
            showToast('No cycles detected in the graph', 'info');
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
// FILTERING
// ============================================================================

export function setFilter(key, value) {
    store.filters[key] = value;
    const graphData = prepareGraphData();
    store.graph.graphData(graphData);
    dispatchEvent('filterChange', { filters: { ...store.filters } });
}

export function clearFilters() {
    store.filters = {
        status: null,
        priority: null,
        labels: [],
        search: '',
        showClosed: true  // Reset to showing all issues
    };
    const graphData = prepareGraphData();
    store.graph.graphData(graphData);
    dispatchEvent('filterChange', { filters: { ...store.filters } });
}

export function search(term) {
    setFilter('search', term);
}

// ============================================================================
// VIEW CONTROLS
// ============================================================================

export function focusNode(nodeId, zoom = 2) {
    const graphData = store.graph.graphData();
    const node = graphData.nodes.find(n => n.id === nodeId);
    if (node) {
        store.graph.centerAt(node.x, node.y, 500);
        store.graph.zoom(zoom, 500);
        selectNode(node);
    }
}

export function zoomToFit(padding = 50) {
    store.graph.zoomToFit(400, padding);
}

export function resetView() {
    clearSelection();
    clearFilters();
    store.graph.centerAt(0, 0, 500);
    store.graph.zoom(1, 500);
}

export function setViewMode(mode) {
    store.viewMode = mode;

    // Deactivate galaxy mode if switching away from it
    if (mode !== VIEW_MODES.LABEL_GALAXY && labelClusterState.active) {
        deactivateLabelGalaxy();
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

    dispatchEvent('viewModeChange', { mode });
}

/**
 * Deactivate label galaxy mode (bv-qpt0)
 */
function deactivateLabelGalaxy() {
    labelClusterState.active = false;
    labelClusterState.clusterHulls.clear();
    hideLabelLegend();
    store.graph?.refresh();
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

    // Update view mode if specified
    if (preset.viewMode && preset.viewMode !== store.viewMode) {
        setViewMode(preset.viewMode);
    }

    // Apply custom forces if defined
    if (preset.customForces) {
        applyCustomForces(preset.customForces);
    } else {
        // Reset to standard force configuration
        store.graph
            .d3Force('link', d3.forceLink()
                .id(d => d.id)
                .distance(getLinkDistance))
            .d3Force('charge', d3.forceManyBody()
                .strength(store.config.chargeStrength))
            .d3Force('center', d3.forceCenter())
            .d3Force('x', d3.forceX()
                .strength(store.config.centerStrength))
            .d3Force('y', d3.forceY()
                .strength(store.config.centerStrength))
            .warmupTicks(store.config.warmupTicks)
            .cooldownTicks(store.config.cooldownTicks)
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

function setupKeyboardShortcuts() {
    document.addEventListener('keydown', (e) => {
        // Ignore if typing in input
        if (e.target.tagName === 'INPUT' || e.target.tagName === 'TEXTAREA') return;

        switch (e.key) {
            case 'Escape':
                if (whatIfState.active) {
                    resetWhatIf();
                } else if (criticalPathState.active) {
                    resetCriticalPath();
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
            case 'y':
                toggleCycleNavigator();
                break;
            case 'h':
                // Toggle heatmap mode
                toggleHeatmap();
                break;
            case '[':
                // Previous cycle
                if (cycleNavigatorState.active) {
                    prevCycle();
                }
                break;
            case ']':
                // Next cycle
                if (cycleNavigatorState.active) {
                    nextCycle();
                }
                break;
            case '?':
                dispatchEvent('helpRequest');
                break;
            case 't':
                // Toggle time-travel mode (bv-z38b)
                if (timeTravelState.history) {
                    if (timeTravelState.active) {
                        stopTimeTravel();
                    } else {
                        startTimeTravel();
                    }
                }
                break;
            case ' ':
                // Space to play/pause time-travel
                if (timeTravelState.active) {
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
    });
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
        .filter(n => n.status !== 'closed') // Only open/in-progress
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

function showTooltip(node) {
    if (!tooltipEl) {
        tooltipEl = document.createElement('div');
        tooltipEl.className = 'bv-graph-tooltip';
        tooltipEl.style.cssText = `
            position: fixed;
            background: ${THEME.bgSecondary};
            color: ${THEME.fg};
            padding: 12px 16px;
            border-radius: 8px;
            border: 1px solid ${THEME.accent.purple};
            font-family: 'JetBrains Mono', monospace;
            font-size: 12px;
            max-width: 320px;
            pointer-events: none;
            z-index: 1000;
            box-shadow: 0 8px 32px rgba(0,0,0,0.4);
            transition: opacity 0.15s;
        `;
        document.body.appendChild(tooltipEl);
    }

    const icon = TYPE_ICONS[node.type] || TYPE_ICONS.default;
    const statusColor = THEME.status[node.status];
    const priorityColor = THEME.priority[node.priority];

    // Format slack as human-readable time
    const formatSlack = (slack) => {
        if (slack === undefined || slack === null || !isFinite(slack)) return '—';
        if (isNaN(slack)) return '—';
        if (slack < 1) return `${Math.round(slack * 24)}h`;
        if (slack < 7) return `${slack.toFixed(1)}d`;
        return `${Math.round(slack / 7)}w`;
    };

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
    if (node.slack !== undefined && isFinite(node.slack)) {
        const slackColor = node.slack < 3 ? THEME.priority.p0 : node.slack < 7 ? THEME.priority.p1 : THEME.fgMuted;
        extendedMetrics.push(`<span style="color: ${slackColor}" title="Schedule flexibility">Slack: ${formatSlack(node.slack)}</span>`);
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
    const x = e.clientX + 15;
    const y = e.clientY + 15;
    tooltipEl.style.left = `${Math.min(x, window.innerWidth - 340)}px`;
    tooltipEl.style.top = `${Math.min(y, window.innerHeight - 200)}px`;
}

function hideTooltip() {
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
        position: fixed;
        top: 60px;
        right: 16px;
        background: ${THEME.bgSecondary}ee;
        color: ${THEME.fg};
        padding: 12px 16px;
        border-radius: 8px;
        border: 1px solid ${THEME.accent.purple};
        font-family: 'JetBrains Mono', monospace;
        font-size: 11px;
        max-height: calc(100vh - 120px);
        overflow-y: auto;
        z-index: 900;
        box-shadow: 0 4px 24px rgba(0,0,0,0.3);
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
    title.innerHTML = `
        <span>Labels</span>
        <span style="cursor: pointer; opacity: 0.6;" onclick="window.bvGraph?.hideLabelLegend()">×</span>
    `;
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

    document.body.appendChild(labelLegendEl);
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
    document.dispatchEvent(new CustomEvent(`bv-graph:${name}`, { detail }));
}

/**
 * Show a toast notification (delegates to parent viewer)
 */
function showToast(message, type = 'info') {
    // Dispatch event for parent viewer to handle
    window.dispatchEvent(new CustomEvent('show-toast', {
        detail: { message, type, id: Date.now() }
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
    store.graph?.refresh();
}

export function cleanup() {
    hideTooltip();
    if (tooltipEl) {
        tooltipEl.remove();
        tooltipEl = null;
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
    lastFrameTime: 0,
    originalNodes: [],    // Snapshot of nodes before time-travel
    originalLinks: [],    // Snapshot of links before time-travel
    nodeStates: new Map(), // node.id -> { visible, opacity, animation }
    controlsEl: null,
};

/**
 * Initialize time-travel with history data
 * @param {Object} history - History data from --robot-history
 */
export function initTimeTravel(history) {
    if (!history || !history.commits || history.commits.length === 0) {
        console.warn('[TimeTravel] No history data provided');
        return false;
    }

    // Sort commits by date
    history.commits.sort((a, b) => new Date(a.date) - new Date(b.date));

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

    // Add styles
    const style = document.createElement('style');
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
            z-index: 1000;
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
    document.head.appendChild(style);

    // Append controls to graph container
    if (store.container) {
        store.container.appendChild(controls);
    }

    timeTravelState.controlsEl = controls;

    // Setup event listeners
    setupTimeTravelListeners();
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
            const idx = Math.round((e.target.value / 100) * (timeTravelState.history.commits.length - 1));
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

    // Save original state
    const graphData = store.graph?.graphData() || { nodes: [], links: [] };
    timeTravelState.originalNodes = [...graphData.nodes];
    timeTravelState.originalLinks = [...graphData.links];
    timeTravelState.nodeStates.clear();

    // Initialize all nodes as hidden
    graphData.nodes.forEach(node => {
        timeTravelState.nodeStates.set(node.id, {
            visible: false,
            opacity: 0,
            animation: null
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
 * Stop time-travel mode and restore original state
 */
export function stopTimeTravel() {
    if (!timeTravelState.active) return;

    // Stop playing
    if (timeTravelState.playing) {
        togglePlay();
    }

    // Restore original nodes
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
 * Go to a specific commit index
 * @param {number} idx - Commit index
 */
function goToCommit(idx) {
    if (!timeTravelState.history || !timeTravelState.active) return;

    const commits = timeTravelState.history.commits;
    idx = Math.max(0, Math.min(idx, commits.length - 1));
    timeTravelState.currentIdx = idx;

    // Calculate which nodes should be visible at this point
    const visibleNodes = new Set();
    const visibleLinks = new Set();

    // Walk through history up to current index
    for (let i = 0; i <= idx; i++) {
        const commit = commits[i];

        // Add nodes from this commit
        if (commit.beads_added) {
            commit.beads_added.forEach(id => visibleNodes.add(id));
        }

        // Remove closed nodes
        if (commit.beads_closed) {
            commit.beads_closed.forEach(id => visibleNodes.delete(id));
        }
    }

    // Update node visibility with animation
    const currentCommit = commits[idx];
    timeTravelState.nodeStates.forEach((state, nodeId) => {
        const shouldBeVisible = visibleNodes.has(nodeId);
        const wasJustAdded = currentCommit.beads_added?.includes(nodeId);
        const wasJustClosed = currentCommit.beads_closed?.includes(nodeId);

        state.visible = shouldBeVisible;
        state.opacity = shouldBeVisible ? 1 : 0;
        state.animation = wasJustAdded ? 'appear' : (wasJustClosed ? 'disappear' : null);
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
        slider.value = (idx / (commits.length - 1)) * 100;
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

    // Sort events by timestamp
    allEvents.sort((a, b) => new Date(a.timestamp) - new Date(b.timestamp));

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
                beads_modified: []
            });
        }

        const commit = commitMap.get(sha);
        if (event.eventType === 'created') {
            commit.beads_added.push(event.beadId);
        } else if (event.eventType === 'closed') {
            commit.beads_closed.push(event.beadId);
        } else if (event.eventType === 'modified') {
            commit.beads_modified.push(event.beadId);
        }
    });

    // Convert to array sorted by date
    const commits = [...commitMap.values()].sort((a, b) =>
        new Date(a.date) - new Date(b.date)
    );

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
                if (issue && issue.status === 'closed') {
                    commit.beads_closed.push(id);
                    closedSet.add(id);
                }
            });
        }

        commits.push(commit);
    }

    return { commits };
}
