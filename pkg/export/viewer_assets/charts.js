/**
 * bv Charts Dashboard Module (bv-wb6h)
 *
 * Interactive charts for project analytics:
 * - Burndown/burnup progress chart
 * - Label dependency heatmap
 * - Priority distribution pie chart
 * - Type breakdown bar chart
 *
 * Uses Chart.js for standard charts, custom canvas for heatmap.
 *
 * @module bv-charts
 * @version 1.0.0
 */

// ============================================================================
// THEME CONSTANTS (matches graph.js Dracula palette)
// ============================================================================

const DARK_CHART_PALETTE = {
    bg: '#282a36',
    bgSecondary: '#44475a',
    bgTertiary: '#21222c',
    fg: '#f8f8f2',
    fgMuted: '#6272a4',
    gridColor: '#44475a44',
    tooltipBg: '#282a36ee',
    borderColor: '#44475a'
};

const LIGHT_CHART_PALETTE = {
    bg: '#f8fafc',
    bgSecondary: '#e2e8f0',
    bgTertiary: '#f1f5f9',
    fg: '#1e293b',
    fgMuted: '#64748b',
    gridColor: '#cbd5e188',
    tooltipBg: '#fffffff2',
    borderColor: '#cbd5e1'
};

const CHART_THEME = {
    ...DARK_CHART_PALETTE,

    // Status colors
    status: {
        open: '#50FA7B',
        in_progress: '#FFB86C',
        blocked: '#FF5555',
        closed: '#6272A4'
    },

    // Priority colors (0 = critical, 4 = backlog)
    priority: [
        '#FF0000',  // P0 Critical
        '#FF5555',  // P1 High
        '#FFB86C',  // P2 Medium
        '#F1FA8C',  // P3 Low
        '#6272A4'   // P4 Backlog
    ],

    // Type colors
    type: {
        bug: '#FF5555',
        feature: '#BD93F9',
        task: '#8BE9FD',
        epic: '#FF79C6',
        chore: '#6272A4'
    }
};

// Chart.js global defaults
if (typeof Chart !== 'undefined') {
    Chart.defaults.color = CHART_THEME.fg;
    Chart.defaults.borderColor = CHART_THEME.borderColor;
    Chart.defaults.font.family = "'JetBrains Mono', monospace";
}

// ============================================================================
// STATE MANAGEMENT
// ============================================================================

const chartsState = {
    burndownChart: null,
    priorityChart: null,
    typeChart: null,
    heatmapCanvas: null,
    issues: [],
    dependencies: [],
    resolvedIds: new Set(),
    initialized: false
};

function normalizeResolvedIds(resolvedIds) {
    if (resolvedIds instanceof Set) return new Set(resolvedIds);
    if (Array.isArray(resolvedIds)) return new Set(resolvedIds);
    if (resolvedIds && typeof resolvedIds[Symbol.iterator] === 'function') return new Set(resolvedIds);
    return new Set();
}

// ============================================================================
// INITIALIZATION
// ============================================================================

/**
 * Initialize the charts dashboard with issue data
 * @param {Array} issues - Array of issue objects
 * @param {Array} dependencies - Array of dependency objects
 * @param {Set<string>|Array<string>} [resolvedIds] - Resolved (closed/tombstone) issue IDs
 */
function initCharts(issues, dependencies, resolvedIds) {
    // Destroy existing charts to prevent "Canvas already in use" errors
    if (chartsState.initialized) {
        destroyCharts();
    }

    chartsState.issues = issues || [];
    chartsState.dependencies = dependencies || [];
    chartsState.resolvedIds = normalizeResolvedIds(resolvedIds);
    chartsState.initialized = true;

    const initialDark = typeof document !== 'undefined'
        ? document.documentElement.classList.contains('dark')
        : true;
    applyTheme(initialDark);

    // Initialize all charts
    initBurndownChart();
    initPriorityChart();
    initTypeChart();
    initHeatmap();

    console.log('[bv-charts] Dashboard initialized with', issues.length, 'issues');
}

/**
 * Sync Chart.js defaults, active chart instances, and custom heatmap canvas with light/dark theme
 * @param {boolean} isDark - Whether dark mode is active
 */
function applyTheme(isDark = true) {
    Object.assign(CHART_THEME, isDark ? DARK_CHART_PALETTE : LIGHT_CHART_PALETTE);

    if (typeof Chart !== 'undefined') {
        Chart.defaults.color = CHART_THEME.fg;
        Chart.defaults.borderColor = CHART_THEME.borderColor;
    }

    if (chartsState.burndownChart) {
        const opts = chartsState.burndownChart.options;
        if (opts.plugins?.legend?.labels) {
            opts.plugins.legend.labels.color = CHART_THEME.fg;
        }
        if (opts.plugins?.tooltip) {
            opts.plugins.tooltip.backgroundColor = CHART_THEME.tooltipBg;
            opts.plugins.tooltip.titleColor = CHART_THEME.fg;
            opts.plugins.tooltip.bodyColor = CHART_THEME.fg;
            opts.plugins.tooltip.borderColor = CHART_THEME.borderColor;
        }
        if (opts.scales?.x) {
            if (opts.scales.x.grid) opts.scales.x.grid.color = CHART_THEME.gridColor;
            if (opts.scales.x.ticks) opts.scales.x.ticks.color = CHART_THEME.fg;
        }
        if (opts.scales?.y) {
            if (opts.scales.y.grid) opts.scales.y.grid.color = CHART_THEME.gridColor;
            if (opts.scales.y.ticks) opts.scales.y.ticks.color = CHART_THEME.fg;
        }
        chartsState.burndownChart.update('none');
    }

    if (chartsState.priorityChart) {
        const ds = chartsState.priorityChart.data?.datasets?.[0];
        if (ds) {
            ds.borderColor = CHART_THEME.bg;
            ds.hoverBorderColor = CHART_THEME.fg;
        }
        const opts = chartsState.priorityChart.options;
        if (opts.plugins?.legend?.labels) {
            opts.plugins.legend.labels.color = CHART_THEME.fg;
        }
        if (opts.plugins?.tooltip) {
            opts.plugins.tooltip.backgroundColor = CHART_THEME.tooltipBg;
            opts.plugins.tooltip.titleColor = CHART_THEME.fg;
            opts.plugins.tooltip.bodyColor = CHART_THEME.fg;
            opts.plugins.tooltip.borderColor = CHART_THEME.borderColor;
        }
        chartsState.priorityChart.update('none');
    }

    if (chartsState.typeChart) {
        const opts = chartsState.typeChart.options;
        if (opts.plugins?.tooltip) {
            opts.plugins.tooltip.backgroundColor = CHART_THEME.tooltipBg;
            opts.plugins.tooltip.titleColor = CHART_THEME.fg;
            opts.plugins.tooltip.bodyColor = CHART_THEME.fg;
            opts.plugins.tooltip.borderColor = CHART_THEME.borderColor;
        }
        if (opts.scales?.x) {
            if (opts.scales.x.grid) opts.scales.x.grid.color = CHART_THEME.gridColor;
            if (opts.scales.x.ticks) opts.scales.x.ticks.color = CHART_THEME.fg;
        }
        if (opts.scales?.y?.ticks) {
            opts.scales.y.ticks.color = CHART_THEME.fg;
        }
        chartsState.typeChart.update('none');
    }

    if (chartsState.heatmapCanvas) {
        renderHeatmap();
    }
}

/**
 * Update all charts with new data
 * @param {Array} issues - Updated issue array
 * @param {Array} dependencies - Updated dependency array
 * @param {Set<string>|Array<string>} [resolvedIds] - Updated resolved issue IDs
 */
function updateCharts(issues, dependencies, resolvedIds) {
    chartsState.issues = issues || chartsState.issues;
    chartsState.dependencies = dependencies || chartsState.dependencies;
    if (resolvedIds !== undefined) {
        chartsState.resolvedIds = normalizeResolvedIds(resolvedIds);
    }

    updateBurndownChart();
    updatePriorityChart();
    updateTypeChart();
    updateHeatmap();
}

/**
 * Destroy all charts and clean up
 */
function destroyCharts() {
    if (chartsState.burndownChart) {
        chartsState.burndownChart.destroy();
        chartsState.burndownChart = null;
    }
    if (chartsState.priorityChart) {
        chartsState.priorityChart.destroy();
        chartsState.priorityChart = null;
    }
    if (chartsState.typeChart) {
        chartsState.typeChart.destroy();
        chartsState.typeChart = null;
    }
    chartsState.initialized = false;
}

// ============================================================================
// BURNDOWN/BURNUP CHART
// ============================================================================

/**
 * Initialize the burndown chart showing open vs closed over time
 */
function initBurndownChart() {
    const canvas = document.getElementById('burndown-chart');
    if (!canvas) return;

    const ctx = canvas.getContext('2d');
    const data = computeBurndownData();

    chartsState.burndownChart = new Chart(ctx, {
        type: 'line',
        data: {
            labels: data.labels,
            datasets: [
                {
                    label: 'Open',
                    data: data.open,
                    borderColor: CHART_THEME.status.open,
                    backgroundColor: CHART_THEME.status.open + '20',
                    fill: true,
                    tension: 0.3,
                    pointRadius: 3,
                    pointHoverRadius: 6
                },
                {
                    label: 'Closed',
                    data: data.closed,
                    borderColor: CHART_THEME.status.closed,
                    backgroundColor: CHART_THEME.status.closed + '20',
                    fill: true,
                    tension: 0.3,
                    pointRadius: 3,
                    pointHoverRadius: 6
                },
                {
                    label: 'Blocked',
                    data: data.blocked,
                    borderColor: CHART_THEME.status.blocked,
                    backgroundColor: CHART_THEME.status.blocked + '20',
                    fill: false,
                    tension: 0.3,
                    pointRadius: 2,
                    pointHoverRadius: 5,
                    borderDash: [5, 5]
                }
            ]
        },
        options: {
            responsive: true,
            maintainAspectRatio: false,
            interaction: {
                intersect: false,
                mode: 'index'
            },
            plugins: {
                legend: {
                    position: 'top',
                    labels: {
                        usePointStyle: true,
                        padding: 15
                    }
                },
                tooltip: {
                    backgroundColor: CHART_THEME.tooltipBg,
                    titleColor: CHART_THEME.fg,
                    bodyColor: CHART_THEME.fg,
                    borderColor: CHART_THEME.borderColor,
                    borderWidth: 1,
                    padding: 12,
                    displayColors: true
                }
            },
            scales: {
                x: {
                    grid: {
                        color: CHART_THEME.gridColor
                    },
                    ticks: {
                        maxTicksLimit: 10
                    }
                },
                y: {
                    beginAtZero: true,
                    grid: {
                        color: CHART_THEME.gridColor
                    },
                    ticks: {
                        precision: 0
                    }
                }
            }
        }
    });
}

function isChartBlockingDep(dep) {
    return !dep.type || ['blocks', 'conditional-blocks', 'waits-for'].includes(dep.type);
}

function resolveChartsReferenceNowMs() {
    const viewerNow = typeof window !== 'undefined' ? window.beadsViewer?.getReferenceNowMs?.() : undefined;
    if (typeof viewerNow === 'number' && Number.isFinite(viewerNow) && viewerNow > 0) {
        return viewerNow;
    }
    const winNow = typeof window !== 'undefined' ? window.BV_REFERENCE_NOW_MS : undefined;
    if (typeof winNow === 'number' && Number.isFinite(winNow) && winNow > 0) {
        return winNow;
    }
    return Date.now();
}

function resolveClosedDate(issue) {
    const rawClosed = issue.closed_at || issue.closedAt;
    if (rawClosed) {
        const parsed = new Date(rawClosed);
        if (!isNaN(parsed)) return parsed;
    }
    if (String(issue.status || '').toLowerCase() === 'closed') {
        const rawUpdated = issue.updated_at || issue.updatedAt;
        if (rawUpdated) {
            const parsed = new Date(rawUpdated);
            if (!isNaN(parsed)) return parsed;
        }
    }
    return null;
}

/**
 * Compute burndown data from issues
 * Groups by day and computes cumulative counts (excluding tombstones and parked deferred items)
 */
function computeBurndownData() {
    const issues = chartsState.issues.filter(i => {
        const st = String(i.status || '').toLowerCase();
        return st !== 'tombstone' && st !== 'deferred';
    });
    if (!issues.length) {
        return { labels: [], open: [], closed: [], blocked: [] };
    }

    // Precompute lifecycle timestamps for all issues (including tombstones so upstream refs resolve)
    const lifecycleById = new Map();
    chartsState.issues.forEach(issue => {
        const created = new Date(issue.created_at || issue.createdAt);
        const rawUpdated = issue.updated_at || issue.updatedAt;
        const updated = rawUpdated ? new Date(rawUpdated) : null;
        lifecycleById.set(issue.id, {
            created: Number.isNaN(created.getTime()) ? null : created,
            updatedAt: updated && !Number.isNaN(updated.getTime()) ? updated : null,
            closedAt: resolveClosedDate(issue),
            status: String(issue.status || 'open').toLowerCase()
        });
    });

    // Map blocking upstream dependencies per issue
    const blockingUpstream = new Map();
    (chartsState.dependencies || []).forEach(dep => {
        if (!isChartBlockingDep(dep)) return;
        if (!blockingUpstream.has(dep.issue_id)) {
            blockingUpstream.set(dep.issue_id, []);
        }
        blockingUpstream.get(dep.issue_id).push(dep.depends_on_id);
    });

    // Get date range
    const dates = issues
        .map(i => lifecycleById.get(i.id)?.created)
        .filter(Boolean);

    if (!dates.length) {
        return { labels: [], open: [], closed: [], blocked: [] };
    }

    const minDate = new Date(Math.min(...dates));
    const refNow = new Date(resolveChartsReferenceNowMs());
    const maxTimestamp = Math.max(
        refNow.getTime(),
        minDate.getTime(),
        ...issues.map(i => lifecycleById.get(i.id)?.closedAt?.getTime()).filter(Number.isFinite)
    );
    const maxDate = new Date(maxTimestamp);

    // Generate daily buckets
    const buckets = new Map();
    const current = new Date(Date.UTC(minDate.getUTCFullYear(), minDate.getUTCMonth(), minDate.getUTCDate()));
    const endDay = new Date(Date.UTC(maxDate.getUTCFullYear(), maxDate.getUTCMonth(), maxDate.getUTCDate()));
    while (current <= endDay) {
        const key = formatDateKey(current);
        buckets.set(key, { open: 0, closed: 0, blocked: 0 });
        current.setUTCDate(current.getUTCDate() + 1);
    }

    const finalDateKey = formatDateKey(endDay);

    // Count issues at each historical day end (dateEnd)
    buckets.forEach((counts, dateKey) => {
        const dateEnd = new Date(`${dateKey}T23:59:59.999Z`);
        const isCurrentBucket = dateKey === finalDateKey;
        issues.forEach(issue => {
            const lc = lifecycleById.get(issue.id);
            if (!lc?.created || lc.created > dateEnd) {
                return; // Not yet created on dateEnd
            }

            if (lc.closedAt && lc.closedAt <= dateEnd) {
                counts.closed++;
                return; // Already closed on dateEnd; never count as open or blocked
            }

            // Issue was open on dateEnd: check whether it was blocked on dateEnd
            const upstreams = blockingUpstream.get(issue.id) || [];
            const hasOpenBlockerOnDate = upstreams.some(depId => {
                const depLc = lifecycleById.get(depId);
                if (!depLc) {
                    if (chartsState.resolvedIds && chartsState.resolvedIds.has(depId)) {
                        return false; // Omitted closed/tombstone prerequisite is resolved
                    }
                    return true; // Unknown/missing prerequisite remains unresolved
                }
                if (String(depLc.status).toLowerCase() === 'tombstone') return false;
                if (depLc.created && depLc.created > dateEnd) return false;
                if (depLc.closedAt && depLc.closedAt <= dateEnd) return false;
                return String(depLc.status).toLowerCase() !== 'closed' || Boolean(depLc.closedAt && depLc.closedAt > dateEnd);
            });

            // Only count explicit status === 'blocked' on buckets >= updatedAt (or the final/current bucket),
            // never projecting current manual blocked status backward before updatedAt.
            const explicitlyBlockedOnDate = lc.status === 'blocked' && (
                lc.updatedAt ? (dateEnd >= lc.updatedAt || isCurrentBucket) : isCurrentBucket
            );
            const isBlockedOnDate = hasOpenBlockerOnDate || explicitlyBlockedOnDate;
            if (isBlockedOnDate) {
                counts.blocked++;
            } else {
                counts.open++;
            }
        });
    });

    // Convert to arrays
    const labels = [];
    const open = [];
    const closed = [];
    const blocked = [];

    // Sample every N days if too many data points
    const entries = [...buckets.entries()];
    const sampleRate = Math.max(1, Math.floor(entries.length / 30));

    entries.forEach(([date, counts], i) => {
        if (i % sampleRate === 0 || i === entries.length - 1) {
            labels.push(formatDateLabel(date));
            open.push(counts.open);
            closed.push(counts.closed);
            blocked.push(counts.blocked);
        }
    });

    return { labels, open, closed, blocked };
}

function updateBurndownChart() {
    if (!chartsState.burndownChart) return;

    const data = computeBurndownData();
    chartsState.burndownChart.data.labels = data.labels;
    chartsState.burndownChart.data.datasets[0].data = data.open;
    chartsState.burndownChart.data.datasets[1].data = data.closed;
    chartsState.burndownChart.data.datasets[2].data = data.blocked;
    chartsState.burndownChart.update();
}

// ============================================================================
// PRIORITY DISTRIBUTION CHART
// ============================================================================

function initPriorityChart() {
    const canvas = document.getElementById('priority-chart');
    if (!canvas) return;

    const ctx = canvas.getContext('2d');
    const data = computePriorityData();

    chartsState.priorityChart = new Chart(ctx, {
        type: 'doughnut',
        data: {
            labels: data.labels,
            datasets: [{
                data: data.counts,
                backgroundColor: data.colors,
                borderColor: CHART_THEME.bg,
                borderWidth: 2,
                hoverBorderColor: CHART_THEME.fg,
                hoverBorderWidth: 3
            }]
        },
        options: {
            responsive: true,
            maintainAspectRatio: false,
            cutout: '60%',
            plugins: {
                legend: {
                    position: 'right',
                    labels: {
                        usePointStyle: true,
                        padding: 12,
                        color: CHART_THEME.fg,
                        generateLabels: (chart) => {
                            const data = chart.data;
                            return data.labels.map((label, i) => ({
                                text: `${label} (${data.datasets[0].data[i]})`,
                                fillStyle: data.datasets[0].backgroundColor[i],
                                strokeStyle: CHART_THEME.bg,
                                fontColor: CHART_THEME.fg,
                                lineWidth: 1,
                                pointStyle: 'circle',
                                hidden: false,
                                index: i
                            }));
                        }
                    }
                },
                tooltip: {
                    backgroundColor: CHART_THEME.tooltipBg,
                    titleColor: CHART_THEME.fg,
                    bodyColor: CHART_THEME.fg,
                    borderColor: CHART_THEME.borderColor,
                    borderWidth: 1,
                    padding: 12,
                    callbacks: {
                        label: (context) => {
                            const total = context.dataset.data.reduce((a, b) => a + b, 0);
                            const pct = total > 0 ? ((context.raw / total) * 100).toFixed(1) : '0.0';
                            return `${context.label}: ${context.raw} (${pct}%)`;
                        }
                    }
                }
            },
            onClick: (event, elements) => {
                if (elements.length > 0) {
                    const priority = elements[0].index;
                    dispatchChartEvent('priorityClick', { priority });
                }
            }
        }
    });
}

function computePriorityData() {
    const issues = chartsState.issues.filter(i => {
        const st = String(i.status || '').toLowerCase();
        return st !== 'closed' && st !== 'tombstone';
    });
    const counts = [0, 0, 0, 0, 0]; // P0-P4

    issues.forEach(issue => {
        const p = Math.max(0, Math.min(4, issue.priority ?? 2));
        counts[p]++;
    });

    return {
        labels: ['P0 Critical', 'P1 High', 'P2 Medium', 'P3 Low', 'P4 Backlog'],
        counts,
        colors: CHART_THEME.priority
    };
}

function updatePriorityChart() {
    if (!chartsState.priorityChart) return;

    const data = computePriorityData();
    chartsState.priorityChart.data.datasets[0].data = data.counts;
    chartsState.priorityChart.update();
}

// ============================================================================
// TYPE BREAKDOWN CHART
// ============================================================================

function initTypeChart() {
    const canvas = document.getElementById('type-chart');
    if (!canvas) return;

    const ctx = canvas.getContext('2d');
    const data = computeTypeData();

    chartsState.typeChart = new Chart(ctx, {
        type: 'bar',
        data: {
            labels: data.labels,
            datasets: [{
                label: 'Issues by Type',
                data: data.counts,
                backgroundColor: data.colors,
                borderColor: data.colors.map(c => c + 'aa'),
                borderWidth: 1,
                borderRadius: 4,
                hoverBackgroundColor: data.colors.map(c => c + 'dd')
            }]
        },
        options: {
            responsive: true,
            maintainAspectRatio: false,
            indexAxis: 'y', // Horizontal bars
            plugins: {
                legend: {
                    display: false
                },
                tooltip: {
                    backgroundColor: CHART_THEME.tooltipBg,
                    titleColor: CHART_THEME.fg,
                    bodyColor: CHART_THEME.fg,
                    borderColor: CHART_THEME.borderColor,
                    borderWidth: 1,
                    padding: 12
                }
            },
            scales: {
                x: {
                    beginAtZero: true,
                    grid: {
                        color: CHART_THEME.gridColor
                    },
                    ticks: {
                        precision: 0
                    }
                },
                y: {
                    grid: {
                        display: false
                    }
                }
            },
            onClick: (event, elements, chart) => {
                if (elements.length > 0) {
                    const typeIndex = elements[0].index;
                    const activeChart = chart || chartsState.typeChart;
                    const rawLabel = activeChart?.data?.labels?.[typeIndex] ?? data.labels[typeIndex];
                    if (rawLabel) {
                        dispatchChartEvent('typeClick', { type: String(rawLabel).toLowerCase() });
                    }
                }
            }
        }
    });
}

function computeTypeData() {
    const activeIssues = chartsState.issues.filter(i => {
        const st = String(i.status || '').toLowerCase();
        return st !== 'closed' && st !== 'tombstone';
    });
    const issues = activeIssues.length > 0
        ? activeIssues
        : chartsState.issues.filter(i => String(i.status || '').toLowerCase() !== 'tombstone');
    const typeCounts = new Map();

    issues.forEach(issue => {
        const type = (issue.type || issue.issue_type || 'task').toLowerCase();
        typeCounts.set(type, (typeCounts.get(type) || 0) + 1);
    });

    // Sort by count descending
    const sorted = [...typeCounts.entries()].sort((a, b) => b[1] - a[1]);

    const labels = sorted.map(([type]) => capitalize(type));
    const counts = sorted.map(([, count]) => count);
    const colors = sorted.map(([type]) => CHART_THEME.type[type] || CHART_THEME.fgMuted);

    return { labels, counts, colors };
}

function updateTypeChart() {
    if (!chartsState.typeChart) return;

    const data = computeTypeData();
    chartsState.typeChart.data.labels = data.labels;
    chartsState.typeChart.data.datasets[0].data = data.counts;
    chartsState.typeChart.data.datasets[0].backgroundColor = data.colors;
    chartsState.typeChart.update();
}

// ============================================================================
// LABEL DEPENDENCY HEATMAP
// ============================================================================

function initHeatmap() {
    const container = document.getElementById('heatmap-container');
    if (!container) return;

    // Create canvas if not exists, clearing any placeholder text first
    let canvas = container.querySelector('canvas');
    if (!canvas) {
        container.replaceChildren();
        canvas = document.createElement('canvas');
        canvas.id = 'heatmap-canvas';
        container.appendChild(canvas);
    } else {
        Array.from(container.children).forEach(child => {
            if (child !== canvas) child.remove();
        });
    }

    chartsState.heatmapCanvas = canvas;
    renderHeatmap();
}

function renderHeatmap() {
    const canvas = chartsState.heatmapCanvas;
    if (!canvas) return;

    const matrix = computeLabelMatrix();
    const truncNote = document.getElementById('heatmap-truncation-note');
    if (truncNote) {
        if (matrix.truncated && matrix.totalLabels > matrix.labels.length) {
            truncNote.textContent = `(Top ${matrix.labels.length} of ${matrix.totalLabels} active labels)`;
            truncNote.classList.remove('hidden');
        } else {
            truncNote.textContent = '';
            truncNote.classList.add('hidden');
        }
    }

    if (!matrix.labels.length) {
        // Show empty state
        const ctx = canvas.getContext('2d');
        canvas.width = canvas.parentElement?.clientWidth || 300;
        canvas.height = 100;
        ctx.fillStyle = CHART_THEME.fgMuted;
        ctx.font = "12px 'JetBrains Mono', monospace";
        ctx.textAlign = 'center';
        ctx.fillText('No label dependencies to display', canvas.width / 2, 50);
        return;
    }

    const { labels, data, maxValue } = matrix;
    const cellSize = Math.max(14, Math.min(40, Math.floor(300 / labels.length)));
    const padding = { top: 60, right: 20, bottom: 20, left: 100 };

    canvas.width = padding.left + labels.length * cellSize + padding.right;
    canvas.height = padding.top + labels.length * cellSize + padding.bottom;

    const ctx = canvas.getContext('2d');
    ctx.fillStyle = CHART_THEME.bg;
    ctx.fillRect(0, 0, canvas.width, canvas.height);

    // Draw cells
    for (let i = 0; i < labels.length; i++) {
        for (let j = 0; j < labels.length; j++) {
            const value = data[i][j];
            const x = padding.left + j * cellSize;
            const y = padding.top + i * cellSize;

            // Color intensity based on value
            const intensity = maxValue > 0 ? value / maxValue : 0;
            const color = interpolateColor(CHART_THEME.bg, CHART_THEME.status.open, intensity);

            ctx.fillStyle = color;
            ctx.fillRect(x + 1, y + 1, cellSize - 2, cellSize - 2);

            // Show value if non-zero
            if (value > 0) {
                ctx.fillStyle = intensity > 0.5 ? CHART_THEME.bg : CHART_THEME.fg;
                ctx.font = `${Math.min(12, Math.max(8, Math.floor(cellSize / 2)))}px 'JetBrains Mono', monospace`;
                ctx.textAlign = 'center';
                ctx.textBaseline = 'middle';
                ctx.fillText(String(value), x + cellSize / 2, y + cellSize / 2);
            }
        }
    }

    // Draw labels (rows - left side)
    ctx.fillStyle = CHART_THEME.fg;
    ctx.font = "10px 'JetBrains Mono', monospace";
    ctx.textAlign = 'right';
    ctx.textBaseline = 'middle';
    labels.forEach((label, i) => {
        const y = padding.top + i * cellSize + cellSize / 2;
        const truncated = truncateLabel(label, 12);
        ctx.fillText(truncated, padding.left - 5, y);
    });

    // Draw labels (columns - top)
    ctx.save();
    ctx.textAlign = 'left';
    labels.forEach((label, j) => {
        const x = padding.left + j * cellSize + cellSize / 2;
        ctx.save();
        ctx.translate(x, padding.top - 5);
        ctx.rotate(-Math.PI / 4);
        const truncated = truncateLabel(label, 10);
        ctx.fillText(truncated, 0, 0);
        ctx.restore();
    });
    ctx.restore();

    // Add click handler (accounts for responsive CSS canvas scaling)
    canvas.onclick = (e) => {
        const rect = canvas.getBoundingClientRect();
        const scaleX = rect.width > 0 ? canvas.width / rect.width : 1;
        const scaleY = rect.height > 0 ? canvas.height / rect.height : 1;
        const x = (e.clientX - rect.left) * scaleX - padding.left;
        const y = (e.clientY - rect.top) * scaleY - padding.top;

        const col = Math.floor(x / cellSize);
        const row = Math.floor(y / cellSize);

        if (row >= 0 && row < labels.length && col >= 0 && col < labels.length) {
            const fromLabel = labels[row];
            const toLabel = labels[col];
            const value = data[row][col];

            if (value > 0) {
                dispatchChartEvent('heatmapClick', {
                    fromLabel,
                    toLabel,
                    count: value
                });
            }
        }
    };
}

function parseChartLabels(raw) {
    if (Array.isArray(raw)) return raw;
    if (typeof raw === 'string' && raw.trim()) {
        try {
            const parsed = JSON.parse(raw);
            if (Array.isArray(parsed)) return parsed;
        } catch {
            // Fall back to empty array on invalid JSON
        }
    }
    return [];
}

function computeLabelMatrix(issuesInput = null, dependenciesInput = null) {
    const sourceIssues = Array.isArray(issuesInput) ? issuesInput : chartsState.issues;
    const sourceDeps = Array.isArray(dependenciesInput) ? dependenciesInput : (chartsState.dependencies || []);
    const issues = sourceIssues.filter(i => String(i.status || '').toLowerCase() !== 'tombstone');
    const deps = sourceDeps.filter(isChartBlockingDep);

    // Build issue -> labels map and per-label issue counts
    const issueLabels = new Map();
    const labelIssueCounts = new Map();
    const labelSet = new Set();

    issues.forEach(issue => {
        const rawLabels = parseChartLabels(issue.labels);
        const uniqueIssueLabels = [...new Set(rawLabels.filter(Boolean))];
        issueLabels.set(issue.id, uniqueIssueLabels);
        uniqueIssueLabels.forEach(l => {
            labelSet.add(l);
            labelIssueCounts.set(l, (labelIssueCounts.get(l) || 0) + 1);
        });
    });

    const uniqueLabels = [...labelSet].sort();
    const totalLabels = uniqueLabels.length;
    if (totalLabels === 0) {
        return { labels: [], data: [], maxValue: 0, totalLabels: 0, truncated: false };
    }

    let labels = uniqueLabels;
    let truncated = false;

    if (totalLabels > 20) {
        // Compute cross-label blocking activity across all labels to pick the top 20 active labels
        const activity = new Map();
        uniqueLabels.forEach(l => activity.set(l, 0));

        deps.forEach(dep => {
            const fromLabels = issueLabels.get(dep.issue_id) || [];
            const toLabels = issueLabels.get(dep.depends_on_id) || [];
            fromLabels.forEach(fromLabel => {
                toLabels.forEach(toLabel => {
                    if (fromLabel !== toLabel) {
                        activity.set(fromLabel, (activity.get(fromLabel) || 0) + 1);
                        activity.set(toLabel, (activity.get(toLabel) || 0) + 1);
                    }
                });
            });
        });

        labels = [...uniqueLabels]
            .sort((a, b) => {
                const actDiff = (activity.get(b) || 0) - (activity.get(a) || 0);
                if (actDiff !== 0) return actDiff;
                const countDiff = (labelIssueCounts.get(b) || 0) - (labelIssueCounts.get(a) || 0);
                if (countDiff !== 0) return countDiff;
                return a.localeCompare(b);
            })
            .slice(0, 20)
            .sort();
        truncated = true;
    }

    const labelIndex = new Map();
    labels.forEach((l, idx) => labelIndex.set(l, idx));

    // Initialize matrix
    const data = labels.map(() => labels.map(() => 0));
    let maxValue = 0;

    // Count cross-label dependencies
    deps.forEach(dep => {
        const fromLabels = issueLabels.get(dep.issue_id) || [];
        const toLabels = issueLabels.get(dep.depends_on_id) || [];

        fromLabels.forEach(fromLabel => {
            const i = labelIndex.get(fromLabel);
            if (i === undefined) return;
            toLabels.forEach(toLabel => {
                if (fromLabel !== toLabel) {
                    const j = labelIndex.get(toLabel);
                    if (j !== undefined) {
                        data[i][j]++;
                        maxValue = Math.max(maxValue, data[i][j]);
                    }
                }
            });
        });
    });

    return { labels, data, maxValue, totalLabels, truncated };
}

function updateHeatmap() {
    renderHeatmap();
}

/**
 * Render an ideal-vs-actual sprint burndown line chart on canvasId using Chart.js.
 */
function renderSprintBurndownChart(canvasId, sprint) {
    if (typeof Chart === 'undefined' || !sprint) return null;
    const canvas = document.getElementById(canvasId);
    if (!canvas) return null;

    const existing = Chart.getChart(canvas);
    if (existing) {
        existing.destroy();
    }

    const bd = sprint.burndown || {};
    const idealLine = Array.isArray(bd.ideal_line) ? bd.ideal_line : [];
    const dailyPoints = Array.isArray(bd.daily_points) ? bd.daily_points : [];
    const totalIssues = Number(bd.total_issues || 0);

    const parseValidISODateParts = (raw) => {
        if (!raw) return null;
        const s = String(raw).trim();
        const m = s.match(/^(\d{4})-(\d{2})-(\d{2})(?:$|[T\s])/);
        if (!m) return null;
        const y = Number(m[1]);
        const mo = Number(m[2]);
        const d = Number(m[3]);
        if (y < 100 || mo < 1 || mo > 12 || d < 1 || d > 31) return null;
        const dt = new Date(Date.UTC(y, mo - 1, d));
        if (dt.getUTCFullYear() !== y || dt.getUTCMonth() !== mo - 1 || dt.getUTCDate() !== d) return null;
        return { key: `${m[1]}-${m[2]}-${m[3]}`, date: dt };
    };
    const dateKey = (raw) => {
        if (!raw) return '';
        const s = String(raw).trim();
        if (/^\d{4}-\d{2}-\d{2}/.test(s)) {
            const parsed = parseValidISODateParts(s);
            return parsed ? parsed.key : '';
        }
        const d = new Date(s);
        if (Number.isNaN(d.getTime()) || d.getUTCFullYear() < 100) return '';
        return d.toISOString().slice(0, 10);
    };

    const labels = [];
    const dateSet = new Set();
    for (const pt of idealLine) {
        const k = dateKey(pt.date);
        if (k && !dateSet.has(k)) { dateSet.add(k); labels.push(k); }
    }
    for (const pt of dailyPoints) {
        const k = dateKey(pt.date);
        if (k && !dateSet.has(k)) { dateSet.add(k); labels.push(k); }
    }
    labels.sort();

    if (labels.length === 0) {
        return null;
    }

    const idealByDate = new Map(idealLine.map(pt => [dateKey(pt.date), Number(pt.remaining)]));
    const actualByDate = new Map(dailyPoints.map(pt => [dateKey(pt.date), Number(pt.remaining)]));

    const idealSeries = labels.map(d => (idealByDate.has(d) ? idealByDate.get(d) : null));
    const actualSeries = labels.map(d => (actualByDate.has(d) ? actualByDate.get(d) : null));

    const isDark = document.documentElement.classList.contains('dark');
    const textColor = isDark ? '#d1d5db' : '#374151';
    const gridColor = isDark ? 'rgba(75, 85, 99, 0.3)' : 'rgba(209, 213, 219, 0.6)';

    return new Chart(canvas.getContext('2d'), {
        type: 'line',
        data: {
            labels: labels.map(formatDateLabel),
            datasets: [
                {
                    label: 'Ideal Remaining',
                    data: idealSeries,
                    borderColor: isDark ? '#94a3b8' : '#64748b',
                    borderDash: [6, 4],
                    borderWidth: 2,
                    pointRadius: 0,
                    tension: 0,
                    spanGaps: true,
                    fill: false,
                },
                {
                    label: 'Actual Remaining',
                    data: actualSeries,
                    borderColor: bd.on_track === false ? '#ef4444' : '#6366f1',
                    backgroundColor: bd.on_track === false ? 'rgba(239, 68, 68, 0.12)' : 'rgba(99, 102, 241, 0.14)',
                    borderWidth: 2.5,
                    pointRadius: 3,
                    pointHoverRadius: 5,
                    tension: 0.15,
                    spanGaps: false,
                    fill: true,
                },
            ],
        },
        options: {
            responsive: true,
            maintainAspectRatio: false,
            interaction: { mode: 'index', intersect: false },
            plugins: {
                legend: {
                    labels: { color: textColor, usePointStyle: true, boxWidth: 8 },
                },
            },
            scales: {
                x: {
                    ticks: { color: textColor, maxTicksLimit: 10 },
                    grid: { color: gridColor },
                },
                y: {
                    beginAtZero: true,
                    suggestedMax: Math.max(totalIssues, 1),
                    ticks: { color: textColor, precision: 0 },
                    grid: { color: gridColor },
                },
            },
        },
    });
}

// ============================================================================
// UTILITIES
// ============================================================================

function formatDateKey(date) {
    return date.toISOString().split('T')[0];
}

function formatDateLabel(dateStr) {
    if (!dateStr) return '';
    const s = String(dateStr).trim();
    if (/^\d{4}-\d{2}-\d{2}/.test(s)) {
        const isoPrefix = s.match(/^(\d{4})-(\d{2})-(\d{2})(?:$|[T\s])/);
        if (!isoPrefix) return '';
        const y = Number(isoPrefix[1]);
        const m = Number(isoPrefix[2]);
        const d = Number(isoPrefix[3]);
        if (y < 100 || m < 1 || m > 12 || d < 1 || d > 31) return '';
        const utcDate = new Date(Date.UTC(y, m - 1, d));
        if (utcDate.getUTCFullYear() !== y || utcDate.getUTCMonth() !== m - 1 || utcDate.getUTCDate() !== d) return '';
        return utcDate.toLocaleDateString('en-US', { month: 'short', day: 'numeric', timeZone: 'UTC' });
    }
    const date = new Date(s);
    if (Number.isNaN(date.getTime()) || date.getUTCFullYear() < 100) return '';
    return date.toLocaleDateString('en-US', { month: 'short', day: 'numeric', timeZone: 'UTC' });
}

function capitalize(str) {
    if (!str) return '';
    return str.charAt(0).toUpperCase() + str.slice(1);
}

function truncateLabel(label, maxLen) {
    if (!label || label.length <= maxLen) return label;
    return label.slice(0, maxLen - 1) + '…';
}

function interpolateColor(color1, color2, factor) {
    // Parse hex colors
    const c1 = parseInt(color1.slice(1), 16);
    const c2 = parseInt(color2.slice(1), 16);

    const r1 = (c1 >> 16) & 0xff;
    const g1 = (c1 >> 8) & 0xff;
    const b1 = c1 & 0xff;

    const r2 = (c2 >> 16) & 0xff;
    const g2 = (c2 >> 8) & 0xff;
    const b2 = c2 & 0xff;

    const r = Math.round(r1 + (r2 - r1) * factor);
    const g = Math.round(g1 + (g2 - g1) * factor);
    const b = Math.round(b1 + (b2 - b1) * factor);

    return `#${((r << 16) | (g << 8) | b).toString(16).padStart(6, '0')}`;
}

function dispatchChartEvent(name, detail) {
    document.dispatchEvent(new CustomEvent(`bv-chart:${name}`, { detail }));
}

// ============================================================================
// PUBLIC API (exposed via window for non-module usage)
// ============================================================================

if (typeof window !== 'undefined') {
    window.bvCharts = {
        init: initCharts,
        update: updateCharts,
        destroy: destroyCharts,
        applyTheme: applyTheme,
        renderHeatmapChart: renderHeatmap,
        renderSprintBurndownChart: renderSprintBurndownChart,
        computeBurndownData: computeBurndownData,
        computeLabelMatrix: computeLabelMatrix,
        CHART_THEME: CHART_THEME
    };
    window.BVCharts = window.bvCharts;
}
