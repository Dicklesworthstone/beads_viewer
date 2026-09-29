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

const CHART_THEME = {
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
    },

    // Chart-specific
    gridColor: '#44475a44',
    tooltipBg: '#282a36ee',
    borderColor: '#44475a'
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
    initialized: false
};

// ============================================================================
// INITIALIZATION
// ============================================================================

/**
 * Initialize the charts dashboard with issue data
 * @param {Array} issues - Array of issue objects
 * @param {Array} dependencies - Array of dependency objects
 */
function initCharts(issues, dependencies) {
    // Destroy existing charts to prevent "Canvas already in use" errors
    if (chartsState.initialized) {
        destroyCharts();
    }

    chartsState.issues = issues || [];
    chartsState.dependencies = dependencies || [];
    chartsState.initialized = true;

    // Initialize all charts
    initBurndownChart();
    initPriorityChart();
    initTypeChart();
    initHeatmap();

    console.log('[bv-charts] Dashboard initialized with', issues.length, 'issues');
}

/**
 * Update all charts with new data
 * @param {Array} issues - Updated issue array
 * @param {Array} dependencies - Updated dependency array
 */
function updateCharts(issues, dependencies) {
    chartsState.issues = issues || chartsState.issues;
    chartsState.dependencies = dependencies || chartsState.dependencies;

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

/**
 * Compute burndown data from issues
 * Groups by day and computes cumulative counts
 */
function computeBurndownData() {
    const issues = chartsState.issues;
    if (!issues.length) {
        return { labels: [], open: [], closed: [], blocked: [] };
    }

    // Get date range
    const dates = issues
        .map(i => new Date(i.created_at || i.createdAt))
        .filter(d => !isNaN(d));

    if (!dates.length) {
        return { labels: [], open: [], closed: [], blocked: [] };
    }

    const minDate = new Date(Math.min(...dates));
    const maxDate = new Date();

    // Generate daily buckets
    const buckets = new Map();
    const current = new Date(minDate);
    while (current <= maxDate) {
        const key = formatDateKey(current);
        buckets.set(key, { open: 0, closed: 0, blocked: 0 });
        current.setDate(current.getDate() + 1);
    }

    // Count issues at each point
    issues.forEach(issue => {
        const created = new Date(issue.created_at || issue.createdAt);
        const status = issue.status || 'open';

        // Issue contributes to count from creation date onward
        buckets.forEach((counts, dateKey) => {
            const bucketDate = new Date(dateKey);
            if (bucketDate >= created) {
                // Check if closed before this date
                let closedAt = null;
                if (issue.closed_at) {
                    closedAt = new Date(issue.closed_at);
                } else if (status === 'closed' && issue.updated_at) {
                    closedAt = new Date(issue.updated_at);
                }

                if (closedAt && bucketDate >= closedAt) {
                    counts.closed++;
                } else if (status === 'blocked') {
                    counts.blocked++;
                } else {
                    counts.open++;
                }
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
                        generateLabels: (chart) => {
                            const data = chart.data;
                            return data.labels.map((label, i) => ({
                                text: `${label} (${data.datasets[0].data[i]})`,
                                fillStyle: data.datasets[0].backgroundColor[i],
                                strokeStyle: CHART_THEME.bg,
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
                            const pct = ((context.raw / total) * 100).toFixed(1);
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
    const issues = chartsState.issues.filter(i => i.status !== 'closed');
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
            onClick: (event, elements) => {
                if (elements.length > 0) {
                    const typeIndex = elements[0].index;
                    const type = data.labels[typeIndex].toLowerCase();
                    dispatchChartEvent('typeClick', { type });
                }
            }
        }
    });
}

function computeTypeData() {
    const issues = chartsState.issues;
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

    // Create canvas if not exists
    let canvas = container.querySelector('canvas');
    if (!canvas) {
        canvas = document.createElement('canvas');
        canvas.id = 'heatmap-canvas';
        container.appendChild(canvas);
    }

    chartsState.heatmapCanvas = canvas;
    renderHeatmap();
}

function renderHeatmap() {
    const canvas = chartsState.heatmapCanvas;
    if (!canvas) return;

    const matrix = computeLabelMatrix();
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
    const cellSize = Math.min(40, Math.floor(300 / labels.length));
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
                ctx.font = `${Math.min(12, cellSize / 3)}px 'JetBrains Mono', monospace`;
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

    // Add click handler
    canvas.onclick = (e) => {
        const rect = canvas.getBoundingClientRect();
        const x = e.clientX - rect.left - padding.left;
        const y = e.clientY - rect.top - padding.top;

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

function computeLabelMatrix() {
    const issues = chartsState.issues;
    const deps = chartsState.dependencies;

    // Build issue -> labels map
    const issueLabels = new Map();
    const labelSet = new Set();

    issues.forEach(issue => {
        const labels = issue.labels || [];
        issueLabels.set(issue.id, labels);
        labels.forEach(l => labelSet.add(l));
    });

    const labels = [...labelSet].sort();
    if (labels.length === 0 || labels.length > 20) {
        // Skip if no labels or too many
        return { labels: [], data: [], maxValue: 0 };
    }

    // Initialize matrix
    const data = labels.map(() => labels.map(() => 0));
    let maxValue = 0;

    // Count cross-label dependencies
    deps.forEach(dep => {
        const fromLabels = issueLabels.get(dep.issue_id) || [];
        const toLabels = issueLabels.get(dep.depends_on_id) || [];

        fromLabels.forEach(fromLabel => {
            toLabels.forEach(toLabel => {
                if (fromLabel !== toLabel) {
                    const i = labels.indexOf(fromLabel);
                    const j = labels.indexOf(toLabel);
                    if (i >= 0 && j >= 0) {
                        data[i][j]++;
                        maxValue = Math.max(maxValue, data[i][j]);
                    }
                }
            });
        });
    });

    return { labels, data, maxValue };
}

function updateHeatmap() {
    renderHeatmap();
}

// ============================================================================
// UTILITIES
// ============================================================================

function formatDateKey(date) {
    return date.toISOString().split('T')[0];
}

function formatDateLabel(dateStr) {
    const date = new Date(dateStr);
    return date.toLocaleDateString('en-US', { month: 'short', day: 'numeric' });
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
        CHART_THEME: CHART_THEME
    };
}
