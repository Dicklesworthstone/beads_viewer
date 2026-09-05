/**
 * Beads Viewer - Static SQL.js WASM-based issue viewer
 *
 * Follows mcp_agent_mail's architecture for client-side sql.js querying with:
 * - OPFS caching for offline support
 * - Chunk reassembly for large databases
 * - FTS5 full-text search
 * - Materialized views for fast queries
 */

// ============================================================================
// Mermaid initialisation (moved here from an inline <script> in index.html so
// the CSP can forbid inline scripts). viewer.js loads after vendor/mermaid.min.js
// and before Alpine boots, which is the same point the inline block ran.
// ============================================================================

// Theme-aware Mermaid config: Dracula-inspired colours in dark mode,
// GitHub-inspired in light mode.
function getMermaidConfig() {
  const dark = document.documentElement.classList.contains('dark');
  return {
    startOnLoad: false,
    theme: dark ? 'dark' : 'neutral',
    flowchart: { curve: 'basis', padding: 10 },
    securityLevel: 'strict',
    themeVariables: dark ? {
      primaryColor: '#bd93f9',      // Purple
      primaryTextColor: '#f8f8f2',  // Foreground
      primaryBorderColor: '#ff79c6', // Pink
      lineColor: '#6272a4',         // Muted (comment)
      secondaryColor: '#44475a',    // Background secondary
      tertiaryColor: '#282a36',     // Background
      background: '#282a36',
      mainBkg: '#282a36',
      nodeBorder: '#bd93f9',
      clusterBkg: '#44475a',
      clusterBorder: '#6272a4',
      titleColor: '#f8f8f2',
      edgeLabelBackground: '#44475a',
      nodeTextColor: '#f8f8f2'
    } : {
      primaryColor: '#8250df',      // Purple
      primaryTextColor: '#24292f',  // Foreground
      primaryBorderColor: '#bf3989', // Pink
      lineColor: '#57606a',         // Muted
      secondaryColor: '#f6f8fa',    // Background secondary
      tertiaryColor: '#ffffff',     // Background
      background: '#ffffff',
      mainBkg: '#ffffff',
      nodeBorder: '#8250df',
      clusterBkg: '#f6f8fa',
      clusterBorder: '#d0d7de',
      titleColor: '#24292f',
      edgeLabelBackground: '#f6f8fa',
      nodeTextColor: '#24292f'
    }
  };
}

if (typeof mermaid !== 'undefined') {
  mermaid.initialize(getMermaidConfig());
} else {
  console.warn('[bv] mermaid is not loaded; diagram rendering is disabled');
}

// Exposed for the theme toggle, which re-initialises Mermaid after switching.
window.reinitializeMermaid = function () {
  if (typeof mermaid !== 'undefined') {
    mermaid.initialize(getMermaidConfig());
  }
};

// ============================================================================
// Error Handling and Diagnostics
// ============================================================================

/**
 * Global error state for the application
 */
const ERROR_STATE = {
  error: null,           // Current error object or null
  errors: [],            // Error history
};

/**
 * Diagnostics state for debugging
 */
const DIAGNOSTICS = {
  wasm: false,           // sql.js WASM loaded
  opfs: null,            // OPFS available (null = not checked, true/false)
  graphWasm: false,      // bv_graph WASM loaded
  hybridWasm: false,     // hybrid scorer WASM loaded
  hybridWasmReason: null, // Reason when hybrid WASM disabled
  dbSource: 'unknown',   // 'network' | 'cache' | 'chunks'
  dbSizeBytes: 0,        // Database size in bytes
  issueCount: 0,         // Number of issues
  loadTimeMs: 0,         // Total load time
  startTime: Date.now(), // When loading started
  queryCount: 0,         // Number of queries executed
  queryErrors: 0,        // Number of query errors
};

/**
 * Show an error to the user with optional actions
 * @param {Object} options - Error display options
 * @param {string} options.title - Error title
 * @param {string} options.message - User-friendly error message
 * @param {string} [options.details] - Technical details (stack trace, etc)
 * @param {Array} [options.actions] - Array of action buttons
 * @param {boolean} [options.dismissible] - Whether the error can be dismissed (default true)
 */
function showError({ title, message, details = null, actions = [], dismissible = true }) {
  const error = {
    id: Date.now(),
    title,
    message,
    details,
    actions,
    dismissible,
    timestamp: new Date().toISOString(),
  };

  ERROR_STATE.error = error;
  ERROR_STATE.errors.push(error);

  // Log to console for debugging
  console.error(`[Error] ${title}: ${message}`, details);

  return error;
}

/**
 * Clear the current error
 */
function clearError() {
  ERROR_STATE.error = null;
}

/**
 * Safe query wrapper with error handling and fallback
 * @param {string} sql - SQL query
 * @param {Array} params - Query parameters
 * @param {*} fallback - Value to return on error
 * @returns {Object} Query result with success flag
 */
function safeQuery(sql, params = [], fallback = []) {
  if (!DB_STATE.db) {
    console.warn('[safeQuery] Database not loaded');
    return { success: false, data: fallback, error: 'Database not loaded' };
  }

  DIAGNOSTICS.queryCount++;

  try {
    const result = DB_STATE.db.exec(sql, params);
    if (!result.length) return { success: true, data: [] };

    const { columns, values } = result[0];
    const data = values.map(row => {
      const obj = {};
      columns.forEach((col, i) => {
        obj[col] = row[i];
      });
      return obj;
    });

    return { success: true, data };
  } catch (err) {
    DIAGNOSTICS.queryErrors++;
    console.error('[safeQuery] Query failed:', sql, err);
    return { success: false, data: fallback, error: err.message };
  }
}

/**
 * Show a toast notification (non-blocking)
 * @param {string} message - Toast message
 * @param {string} type - Toast type: 'info' | 'success' | 'warning' | 'error'
 */
function showToast(message, type = 'info') {
  // This will be picked up by the Alpine toast component
  window.dispatchEvent(new CustomEvent('show-toast', {
    detail: { message, type, id: Date.now() }
  }));
}

// Database state
const DB_STATE = {
  sql: null,          // sql.js library instance
  db: null,           // Database instance
  cacheKey: null,     // OPFS cache key (hash)
  source: 'unknown',  // 'network' | 'cache' | 'chunks'
  fts5: null,         // Actual browser SQLite capability, not exported schema.
};

// Graph engine state (WASM)
const GRAPH_STATE = {
  wasm: null,         // WASM module (bv_graph.js)
  graph: null,        // DiGraph instance
  nodeMap: null,      // Map<string, number> - issue ID to node index
  ready: false,       // true when graph is loaded
};

// WASM support detection
const WASM_STATUS = {
  supported: null,    // null = not checked, true/false = checked
  reason: null,       // Reason for failure if not supported
  fallbackMode: false, // true when using pre-computed data only
  features: {
    basic: null,      // Basic WASM support
    simd: null,       // SIMD support
    threads: null,    // Thread support (SharedArrayBuffer)
  },
};

/**
 * Check WASM support in the browser
 * @returns {Promise<{supported: boolean, reason: string|null, features: Object}>}
 */
async function checkWASMSupport() {
  // Already checked
  if (WASM_STATUS.supported !== null) {
    return WASM_STATUS;
  }

  // Check basic WASM support
  if (typeof WebAssembly !== 'object') {
    WASM_STATUS.supported = false;
    WASM_STATUS.reason = 'WebAssembly not available in this browser';
    WASM_STATUS.fallbackMode = true;
    return WASM_STATUS;
  }

  // Check WebAssembly APIs
  try {
    const testModule = new WebAssembly.Module(
      new Uint8Array([0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00])
    );
    if (!(testModule instanceof WebAssembly.Module)) {
      throw new Error('WebAssembly.Module validation failed');
    }
    WASM_STATUS.features.basic = true;
  } catch (e) {
    WASM_STATUS.supported = false;
    WASM_STATUS.reason = `WebAssembly validation failed: ${e.message}`;
    WASM_STATUS.fallbackMode = true;
    WASM_STATUS.features.basic = false;
    return WASM_STATUS;
  }

  // Check SharedArrayBuffer (needed for some WASM features)
  WASM_STATUS.features.threads = typeof SharedArrayBuffer !== 'undefined';

  // SIMD detection (optional, not required)
  try {
    WASM_STATUS.features.simd = WebAssembly.validate(new Uint8Array([
      0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00,
      0x01, 0x05, 0x01, 0x60, 0x00, 0x01, 0x7b, 0x03,
      0x02, 0x01, 0x00, 0x0a, 0x0a, 0x01, 0x08, 0x00,
      0xfd, 0x0c, 0x00, 0x00, 0x00, 0x00, 0x0b
    ]));
  } catch {
    WASM_STATUS.features.simd = false;
  }

  WASM_STATUS.supported = true;
  WASM_STATUS.reason = null;
  return WASM_STATUS;
}

/**
 * Enable fallback mode (use pre-computed data only)
 */
function enableFallbackMode(reason) {
  WASM_STATUS.fallbackMode = true;
  WASM_STATUS.reason = reason;
  GRAPH_STATE.ready = false;
  DIAGNOSTICS.graphWasm = false;
  console.warn('[WASM] Fallback mode enabled:', reason);
  showToast('Using pre-computed metrics only', 'warning');
}

// ============================================================================
// WASM Memory Management
// ============================================================================

/**
 * Track allocated WASM objects for cleanup
 * WASM objects allocated via wasm-bindgen are NOT garbage collected by JS
 */
const WASM_ALLOCATIONS = {
  subgraphs: [],   // Temporary subgraph objects
  trackCount: 0,   // Total allocations tracked
  freedCount: 0,   // Total objects freed
};

/**
 * Safely execute a function with a temporary subgraph, ensuring cleanup
 *
 * Example usage:
 *   const result = withSubgraph(indices, subgraph => ({
 *     pagerank: subgraph.pagerankDefault(),
 *     betweenness: subgraph.betweenness(),
 *   }));
 *
 * @param {Uint32Array|number[]} indices - Node indices for the subgraph
 * @param {Function} fn - Function to execute with the subgraph
 * @returns {*} Result from the function
 */
function withSubgraph(indices, fn) {
  if (!GRAPH_STATE.ready || !GRAPH_STATE.graph) {
    console.warn('[WASM Memory] Cannot create subgraph: graph not ready');
    return null;
  }

  const indicesArray = indices instanceof Uint32Array
    ? indices
    : new Uint32Array(indices);

  const subgraph = GRAPH_STATE.graph.subgraph(indicesArray);
  WASM_ALLOCATIONS.trackCount++;

  try {
    return fn(subgraph);
  } finally {
    // Always free the subgraph, even if fn throws
    if (subgraph && typeof subgraph.free === 'function') {
      subgraph.free();
      WASM_ALLOCATIONS.freedCount++;
    }
  }
}

/**
 * Clean up all WASM resources
 * Call on page unload or when reinitializing
 */
function cleanupWasm() {
  console.log('[WASM Memory] Cleaning up resources...');

  // Free tracked subgraphs (shouldn't be any if withSubgraph is used correctly)
  for (const subgraph of WASM_ALLOCATIONS.subgraphs) {
    if (subgraph && typeof subgraph.free === 'function') {
      try {
        subgraph.free();
        WASM_ALLOCATIONS.freedCount++;
      } catch (e) {
        console.warn('[WASM Memory] Error freeing subgraph:', e);
      }
    }
  }
  WASM_ALLOCATIONS.subgraphs = [];

  // Free the main graph
  if (GRAPH_STATE.graph && typeof GRAPH_STATE.graph.free === 'function') {
    try {
      GRAPH_STATE.graph.free();
      console.log('[WASM Memory] Main graph freed');
    } catch (e) {
      console.warn('[WASM Memory] Error freeing main graph:', e);
    }
    GRAPH_STATE.graph = null;
    GRAPH_STATE.ready = false;
    GRAPH_STATE.nodeMap = null;
  }

  console.log(`[WASM Memory] Cleanup complete. Tracked: ${WASM_ALLOCATIONS.trackCount}, Freed: ${WASM_ALLOCATIONS.freedCount}`);
}

/**
 * Get WASM memory statistics for diagnostics
 */
function getWasmMemoryStats() {
  return {
    tracked: WASM_ALLOCATIONS.trackCount,
    freed: WASM_ALLOCATIONS.freedCount,
    pendingSubgraphs: WASM_ALLOCATIONS.subgraphs.length,
    graphActive: GRAPH_STATE.graph !== null,
    leakEstimate: WASM_ALLOCATIONS.trackCount - WASM_ALLOCATIONS.freedCount - WASM_ALLOCATIONS.subgraphs.length,
  };
}

// Register cleanup on page unload
if (typeof window !== 'undefined') {
  window.addEventListener('beforeunload', () => {
    cleanupWasm();
  });

  // Also cleanup on visibility change (mobile browsers)
  document.addEventListener('visibilitychange', () => {
    if (document.visibilityState === 'hidden') {
      // Could optionally free resources here for memory-constrained devices
      // cleanupWasm();
    }
  });
}

/**
 * Initialize sql.js library
 */
async function initSqlJs() {
  if (DB_STATE.sql) {
    DIAGNOSTICS.wasm = true;
    return DB_STATE.sql;
  }

  // Load sql.js from CDN (with WASM)
  const sqlPromise = initSqlJs.cached || (initSqlJs.cached = new Promise(async (resolve, reject) => {
    try {
      let usedLocal = false;
      // Try loading from local vendor first
      let sqlJs;
      try {
        const script = document.createElement('script');
        script.src = './vendor/sql-wasm.js';
        document.head.appendChild(script);
        await new Promise((res, rej) => {
          script.onload = res;
          script.onerror = rej;
        });
        sqlJs = window.initSqlJs;
        usedLocal = true;
      } catch {
        // Fallback to CDN
        const script = document.createElement('script');
        script.src = 'https://cdn.jsdelivr.net/npm/sql.js@1.10.3/dist/sql-wasm.js';
        document.head.appendChild(script);
        await new Promise((res, rej) => {
          script.onload = res;
          script.onerror = rej;
        });
        sqlJs = window.initSqlJs;
        usedLocal = false;
      }

      const SQL = await sqlJs({
        locateFile: file => {
          // Prefer local vendored wasm when available for offline use
          if (usedLocal) {
            return `./vendor/${file}`;
          }
          return `https://cdn.jsdelivr.net/npm/sql.js@1.10.3/dist/${file}`;
        }
      });

      DIAGNOSTICS.wasm = true;
      resolve(SQL);
    } catch (err) {
      DIAGNOSTICS.wasm = false;
      showError({
        title: 'Browser Compatibility Issue',
        message: 'This viewer requires WebAssembly support to run SQL queries.',
        details: err.message,
        actions: [
          { label: 'Check Browser Support', url: 'https://caniuse.com/wasm' },
          { label: 'Reload Page', action: () => location.reload() },
        ],
        dismissible: false,
      });
      reject(err);
    }
  }));

  DB_STATE.sql = await sqlPromise;
  return DB_STATE.sql;
}

/**
 * Load database from OPFS cache
 */
async function loadFromOPFS(cacheKey) {
  if (!('storage' in navigator) || !navigator.storage.getDirectory) {
    DIAGNOSTICS.opfs = false;
    console.info('[OPFS] Not available in this browser');
    return null;
  }

  try {
    const root = await navigator.storage.getDirectory();
    const filename = `beads-${cacheKey || 'default'}.sqlite3`;
    const handle = await root.getFileHandle(filename, { create: false });
    const file = await handle.getFile();
    const buffer = await file.arrayBuffer();
    DIAGNOSTICS.opfs = true;
    DIAGNOSTICS.dbSizeBytes = buffer.byteLength;
    console.log(`[OPFS] Loaded ${buffer.byteLength} bytes from cache`);
    return new Uint8Array(buffer);
  } catch (err) {
    if (err.name === 'NotFoundError') {
      DIAGNOSTICS.opfs = true; // OPFS available, just no cache yet
    } else {
      // Private browsing or permission denied
      DIAGNOSTICS.opfs = false;
      console.info('[OPFS] Cache unavailable:', err.message);
    }
    return null;
  }
}

/**
 * Cache database to OPFS
 */
async function cacheToOPFS(data, cacheKey) {
  if (!('storage' in navigator) || !navigator.storage.getDirectory) {
    return false;
  }

  try {
    const root = await navigator.storage.getDirectory();
    const filename = `beads-${cacheKey || 'default'}.sqlite3`;
    const handle = await root.getFileHandle(filename, { create: true });
    const writable = await handle.createWritable();
    await writable.write(data);
    await writable.close();
    console.log(`[OPFS] Cached ${data.byteLength} bytes`);
    return true;
  } catch (err) {
    console.warn('[OPFS] Cache failed:', err);
    return false;
  }
}

/**
 * Remove stale OPFS cache entries (old hashes and the legacy "default" key).
 * Runs in the background after a successful cache hit to free storage.
 */
async function cleanStaleOPFS(currentCacheKey) {
  if (!('storage' in navigator) || !navigator.storage.getDirectory) return;
  try {
    const root = await navigator.storage.getDirectory();
    const currentFile = `beads-${currentCacheKey}.sqlite3`;
    // Collect stale names first to avoid mutating directory during iteration
    const stale = [];
    for await (const [name] of root.entries()) {
      if (name.startsWith('beads-') && name.endsWith('.sqlite3') && name !== currentFile) {
        stale.push(name);
      }
    }
    for (const name of stale) {
      await root.removeEntry(name);
      console.log(`[OPFS] Removed stale cache: ${name}`);
    }
  } catch (err) {
    // Non-critical; ignore cleanup failures
  }
}

/**
 * Fetch JSON file
 */
async function fetchJSON(url) {
  const response = await fetch(url);
  if (!response.ok) throw new Error(`HTTP ${response.status}`);
  return response.json();
}

/**
 * Load database chunks and reassemble
 */
async function loadChunks(config) {
  const chunks = [];
  const totalChunks = config.chunk_count;
  // Use same cache-buster for all chunks to ensure consistency
  const cacheBuster = `?_t=${Date.now()}`;

  for (let i = 0; i < totalChunks; i++) {
    const chunkPath = `./chunks/${String(i).padStart(5, '0')}.bin${cacheBuster}`;
    const response = await fetch(chunkPath);
    if (!response.ok) throw new Error(`Failed to load chunk ${i}`);
    const buffer = await response.arrayBuffer();
    chunks.push(new Uint8Array(buffer));
  }

  // Concatenate all chunks
  const totalSize = chunks.reduce((sum, c) => sum + c.length, 0);
  const combined = new Uint8Array(totalSize);
  let offset = 0;
  for (const chunk of chunks) {
    combined.set(chunk, offset);
    offset += chunk.length;
  }

  console.log(`[Chunks] Reassembled ${totalChunks} chunks, ${totalSize} bytes`);
  return combined;
}

/**
 * Load database with caching strategy
 */
async function loadDatabase(updateStatus) {
  const SQL = await initSqlJs();

  updateStatus?.('Checking cache...');

  // Load config to get cache key
  // Use cache-busting query param to ensure we always get the latest config
  // This prevents CDN caching issues on GitHub Pages where stale config
  // could cause OPFS to serve an outdated database
  let config = null;
  try {
    const cacheBuster = `?_t=${Date.now()}`;
    config = await fetchJSON('./beads.sqlite3.config.json' + cacheBuster);
    DB_STATE.cacheKey = config.hash || null;
  } catch {
    // Config file may not exist for small DBs
  }

  // Try OPFS cache first
  if (DB_STATE.cacheKey) {
    const cached = await loadFromOPFS(DB_STATE.cacheKey);
    if (cached) {
      DB_STATE.db = new SQL.Database(cached);
      DB_STATE.source = 'cache';
      DIAGNOSTICS.dbSource = 'cache';
      // Clean up stale cache entries in the background
      cleanStaleOPFS(DB_STATE.cacheKey).catch(() => {});
      return DB_STATE.db;
    }
  }

  updateStatus?.('Loading database...');

  // Check if database is chunked
  let dbData;
  try {
    if (config?.chunked) {
      updateStatus?.(`Loading ${config.chunk_count} chunks...`);
      dbData = await loadChunks(config);
      DB_STATE.source = 'chunks';
      DIAGNOSTICS.dbSource = 'chunks';
    } else {
      // Load single file - try multiple paths
      // Add cache-busting to avoid CDN serving stale database
      const cacheBuster = `?_t=${Date.now()}`;
      const paths = ['./beads.sqlite3' + cacheBuster, './data/beads.sqlite3' + cacheBuster];
      let loaded = false;

      for (const path of paths) {
        try {
          const response = await fetch(path);
          if (response.ok) {
            const buffer = await response.arrayBuffer();
            dbData = new Uint8Array(buffer);
            DIAGNOSTICS.dbSizeBytes = buffer.byteLength;
            DB_STATE.source = 'network';
            DIAGNOSTICS.dbSource = 'network';
            loaded = true;
            break;
          }
        } catch {
          // Try next path
        }
      }

      if (!loaded) {
        throw new Error('Database not found at any known path');
      }
    }
  } catch (err) {
    showError({
      title: 'Database Not Found',
      message: 'Could not load the issues database.',
      details: `${err.message}\n\nThe beads.sqlite3 file may be missing or corrupted.`,
      actions: [
        { label: 'Reload Page', action: () => location.reload() },
      ],
      dismissible: false,
    });
    throw err;
  }

  try {
    DB_STATE.db = new SQL.Database(dbData);
  } catch (err) {
    showError({
      title: 'Database Corrupted',
      message: 'The database file could not be opened.',
      details: err.message,
      actions: [
        { label: 'Reload Page', action: () => location.reload() },
      ],
      dismissible: false,
    });
    throw err;
  }

  // Cache for next time and clean up stale entries (e.g., legacy "default" key)
  if (DB_STATE.cacheKey) {
    updateStatus?.('Caching for offline...');
    await cacheToOPFS(DB_STATE.db.export(), DB_STATE.cacheKey);
    cleanStaleOPFS(DB_STATE.cacheKey).catch(() => {});
  }

  return DB_STATE.db;
}

/**
 * Execute a SQL query and return results as array of objects
 */
function execQuery(sql, params = []) {
  if (!DB_STATE.db) throw new Error('Database not loaded');

  try {
    const result = DB_STATE.db.exec(sql, params);
    if (!result.length) return [];

    const { columns, values } = result[0];
    return values.map(row => {
      const obj = {};
      columns.forEach((col, i) => {
        obj[col] = row[i];
      });
      return obj;
    });
  } catch (err) {
    console.error('Query error:', err, sql);
    throw err;
  }
}

/**
 * Get a single value from a query
 */
function execScalar(sql, params = []) {
  const result = execQuery(sql, params);
  if (!result.length) return null;
  return Object.values(result[0])[0];
}

// ============================================================================
// WASM Graph Engine - Live graph calculations
// ============================================================================

/**
 * Initialize the WASM graph engine
 */
async function initGraphEngine() {
  if (GRAPH_STATE.ready) return true;

  // Check if we're already in fallback mode
  if (WASM_STATUS.fallbackMode) {
    console.log('[Graph] Fallback mode active, skipping WASM init');
    return false;
  }

  // Check WASM support first
  await checkWASMSupport();
  if (!WASM_STATUS.supported) {
    enableFallbackMode(WASM_STATUS.reason);
    return false;
  }

  try {
    // Dynamic import of the WASM module
    const wasmModule = await import('./vendor/bv_graph.js');
    await wasmModule.default(); // Initialize WASM

    // Expose for other modules (e.g., graph.js force-graph view) to reuse.
    window.bvGraphWasm = wasmModule;

    GRAPH_STATE.wasm = wasmModule;
    GRAPH_STATE.graph = new wasmModule.DiGraph();
    GRAPH_STATE.nodeMap = new Map();

    // Load graph data from SQLite
    if (!DB_STATE.db) {
      console.warn('[Graph] Database not loaded yet');
      return false;
    }

    // Isolated issues participate in metrics too. Edges alone omit them.
    for (const row of execQuery('SELECT id FROM issues ORDER BY id')) {
      GRAPH_STATE.nodeMap.set(row.id, GRAPH_STATE.graph.addNode(row.id));
    }

    const deps = execQuery(`
      SELECT issue_id, depends_on_id
      FROM dependencies
      WHERE type = 'blocks'
      ORDER BY issue_id, depends_on_id
    `);

    for (const row of deps) {
      const from = row.issue_id;
      const to = row.depends_on_id;

      if (!GRAPH_STATE.nodeMap.has(from)) {
        const idx = GRAPH_STATE.graph.addNode(from);
        GRAPH_STATE.nodeMap.set(from, idx);
      }
      if (!GRAPH_STATE.nodeMap.has(to)) {
        const idx = GRAPH_STATE.graph.addNode(to);
        GRAPH_STATE.nodeMap.set(to, idx);
      }

      GRAPH_STATE.graph.addEdge(
        GRAPH_STATE.nodeMap.get(from),
        GRAPH_STATE.nodeMap.get(to)
      );
    }

    GRAPH_STATE.ready = true;
    WASM_STATUS.fallbackMode = false;
    const nodeCount = GRAPH_STATE.graph.nodeCount();
    const edgeCount = GRAPH_STATE.graph.edgeCount();
    if (nodeCount === 0) {
      console.log('[Graph] WASM engine ready (empty project)');
    } else {
      console.log(`[Graph] WASM engine loaded: ${nodeCount} nodes, ${edgeCount} edges`);
    }
    return true;
  } catch (err) {
    console.warn('[Graph] WASM init failed:', err.message);
    enableFallbackMode(`WASM load failed: ${err.message}`);
    return false;
  }
}

/**
 * Build closed set array from database
 * Returns Uint8Array where 1 = closed, 0 = open
 */
function buildClosedSet() {
  if (!GRAPH_STATE.ready) return null;

  const n = GRAPH_STATE.graph.nodeCount();
  const closed = new Uint8Array(n);

  const closedIssues = execQuery(`
    SELECT id FROM issues WHERE status = 'closed'
  `);

  for (const row of closedIssues) {
    const idx = GRAPH_STATE.nodeMap.get(row.id);
    if (idx !== undefined) {
      closed[idx] = 1;
    }
  }

  return closed;
}

/**
 * Recalculate graph metrics for a filtered set of issues
 */
function recalculateMetrics(issueIds) {
  if (!GRAPH_STATE.ready) return null;

  const start = performance.now();
  const indices = issueIds
    .map(id => GRAPH_STATE.nodeMap.get(id))
    .filter(idx => idx !== undefined);

  if (indices.length === 0) return null;

  // Extract subgraph for filtered issues - use withSubgraph for automatic cleanup
  const result = withSubgraph(indices, subgraph => ({
    nodeCount: subgraph.nodeCount(),
    edgeCount: subgraph.edgeCount(),
    pagerank: subgraph.pagerankDefault(),
    betweenness: subgraph.betweenness(),
    hasCycles: subgraph.hasCycles(),
    criticalPath: subgraph.criticalPathHeights(),
  }));

  const elapsed = performance.now() - start;
  console.log(`[Graph] Recalculated metrics in ${elapsed.toFixed(1)}ms`);

  return result;
}

/**
 * What-if analysis: compute cascade impact of closing an issue
 */
function whatIfClose(issueId) {
  if (!GRAPH_STATE.ready) return null;

  const idx = GRAPH_STATE.nodeMap.get(issueId);
  if (idx === undefined) return null;

  const closedSet = buildClosedSet();
  const result = GRAPH_STATE.graph.whatIfClose(idx, closedSet);

  // Convert node indices back to issue IDs
  if (result && result.cascade_ids) {
    result.cascade_issue_ids = result.cascade_ids
      .map(i => GRAPH_STATE.graph.nodeId(i))
      .filter(Boolean);
  }

  return result;
}

/**
 * Get top issues by cascade impact
 */
function topWhatIf(limit = 10) {
  if (!GRAPH_STATE.ready) return [];

  const closedSet = buildClosedSet();
  const results = GRAPH_STATE.graph.topWhatIf(closedSet, limit);

  // Enrich with issue IDs
  return (results || []).map(item => ({
    ...item,
    issueId: GRAPH_STATE.graph.nodeId(item.node),
    result: item.result,
  }));
}

/**
 * Get actionable issues (all blockers closed)
 */
function getActionableIssues() {
  if (!GRAPH_STATE.ready) return [];

  const closedSet = buildClosedSet();
  const indices = GRAPH_STATE.graph.actionableNodes(closedSet);

  return (indices || [])
    .map(idx => GRAPH_STATE.graph.nodeId(idx))
    .filter(Boolean);
}

/**
 * Find cycle break suggestions
 */
function getCycleBreakSuggestions(limit = 5) {
  if (!GRAPH_STATE.ready) return null;

  const result = GRAPH_STATE.graph.cycleBreakSuggestions(limit, 100);
  return result;
}

/**
 * Get greedy top-k issues to maximize unblocks
 */
function getTopKSet(k = 5) {
  if (!GRAPH_STATE.ready) return null;

  const closedSet = buildClosedSet();
  const result = GRAPH_STATE.graph.topkSet(closedSet, k);

  // Enrich with issue IDs
  if (result && result.items) {
    result.items = result.items.map(item => ({
      ...item,
      issueId: GRAPH_STATE.graph.nodeId(item.node),
      unblocked_issue_ids: (item.unblocked_ids || [])
        .map(i => GRAPH_STATE.graph.nodeId(i))
        .filter(Boolean),
    }));
  }

  return result;
}

// ============================================================================
// Query Layer - Using materialized views for performance
// ============================================================================

/**
 * Build WHERE clauses from filters (shared between query and count)
 */
function buildFilterClauses(filters = {}, tableAlias = '') {
  const clauses = [];
  const params = [];
  const col = (name) => (tableAlias ? `${tableAlias}.${name}` : name);

  // Status filter (supports array for multi-select)
  if (filters.status?.length) {
    const statuses = Array.isArray(filters.status) ? filters.status : [filters.status];
    if (statuses.length === 1) {
      clauses.push(`${col('status')} = ?`);
      params.push(statuses[0]);
    } else {
      clauses.push(`${col('status')} IN (${statuses.map(() => '?').join(',')})`);
      params.push(...statuses);
    }
  }

  // Type filter (supports array for multi-select)
  if (filters.type?.length) {
    const types = Array.isArray(filters.type) ? filters.type : [filters.type];
    if (types.length === 1) {
      clauses.push(`${col('issue_type')} = ?`);
      params.push(types[0]);
    } else {
      clauses.push(`${col('issue_type')} IN (${types.map(() => '?').join(',')})`);
      params.push(...types);
    }
  }

	  // Priority filter (supports array for multi-select)
	  if (filters.priority?.length) {
	    const priorities = (Array.isArray(filters.priority) ? filters.priority : [filters.priority])
	      .map(p => parseInt(p, 10))
	      .filter(p => !isNaN(p));
	    if (priorities.length === 1) {
      clauses.push(`${col('priority')} = ?`);
      params.push(priorities[0]);
    } else if (priorities.length > 1) {
      clauses.push(`${col('priority')} IN (${priorities.map(() => '?').join(',')})`);
      params.push(...priorities);
    }
  }

  // Assignee filter
  if (filters.assignee) {
    clauses.push(`${col('assignee')} = ?`);
    params.push(filters.assignee);
  }

	  // Blocked filter
	  if (filters.hasBlockers === true || filters.hasBlockers === 'true') {
    clauses.push(`(${col('blocked_by_ids')} IS NOT NULL AND ${col('blocked_by_ids')} <> '')`);
  } else if (filters.hasBlockers === false || filters.hasBlockers === 'false') {
    clauses.push(`(${col('blocked_by_ids')} IS NULL OR ${col('blocked_by_ids')} = '')`);
  }

  // Blocking filter (has items depending on it)
  if (filters.isBlocking === true || filters.isBlocking === 'true') {
    clauses.push(`${col('blocks_count')} > 0`);
  }

  // Label filter (JSON array contains)
  if (filters.labels?.length) {
    const labels = Array.isArray(filters.labels) ? filters.labels : [filters.labels];
    const labelClauses = labels.map(() => `${col('labels')} LIKE ?`);
    clauses.push(`(${labelClauses.join(' OR ')})`);
    params.push(...labels.map(l => `%"${l}"%`));
  }

  // Search filter (LIKE-based, FTS5 handled separately)
  if (filters.search) {
    clauses.push(`(${col('title')} LIKE ? OR ${col('description')} LIKE ? OR ${col('id')} LIKE ?)`);
    const searchTerm = `%${filters.search}%`;
    params.push(searchTerm, searchTerm, searchTerm);
  }

  return { clauses, params };
}

/**
 * Query issues with filters, sorting, and pagination
 */
function queryIssues(filters = {}, sort = 'priority', limit = 50, offset = 0) {
  const { clauses, params } = buildFilterClauses(filters);

  let sql = `SELECT * FROM issue_overview_mv`;
  if (clauses.length > 0) {
    sql += ` WHERE ${clauses.join(' AND ')}`;
  }

  // Sorting
  const sortMap = {
    'priority': 'priority ASC, triage_score DESC',
    'updated': 'updated_at DESC',
    'score': 'triage_score DESC',
    'blocks': 'blocks_count DESC',
    'created': 'created_at DESC',
    'title': 'title ASC',
    'id': 'id ASC',
  };
  sql += ` ORDER BY ${sortMap[sort] || sortMap.priority}`;
  sql += ` LIMIT ? OFFSET ?`;
  params.push(limit, offset);

  return execQuery(sql, params);
}

/**
 * Count issues matching filters
 */
function countIssues(filters = {}) {
  const { clauses, params } = buildFilterClauses(filters);

  let sql = `SELECT COUNT(*) as count FROM issue_overview_mv`;
  if (clauses.length > 0) {
    sql += ` WHERE ${clauses.join(' AND ')}`;
  }

  return execScalar(sql, params) || 0;
}

/**
 * Get unique values for filter dropdowns
 */
	function getFilterOptions() {
	  return {
	    statuses: execQuery(`SELECT DISTINCT status FROM issue_overview_mv ORDER BY status`).map(r => r.status),
	    types: execQuery(`SELECT DISTINCT issue_type FROM issue_overview_mv ORDER BY issue_type`).map(r => r.issue_type),
	    priorities: execQuery(`SELECT DISTINCT priority FROM issue_overview_mv ORDER BY priority`).map(r => r.priority),
	    assignees: execQuery(`SELECT DISTINCT assignee FROM issue_overview_mv WHERE assignee IS NOT NULL AND assignee <> '' ORDER BY assignee`).map(r => r.assignee),
	    labels: getUniqueLabels(),
	  };
	}

/**
 * Get unique labels from all issues
 */
	function getUniqueLabels() {
	  const results = execQuery(`SELECT labels FROM issue_overview_mv WHERE labels IS NOT NULL AND labels <> ''`);
	  const labelSet = new Set();
	  for (const row of results) {
	    try {
	      const labels = JSON.parse(row.labels);
	      if (Array.isArray(labels)) {
        labels.forEach(l => labelSet.add(l));
      }
    } catch { /* ignore parse errors */ }
  }
  return Array.from(labelSet).sort();
}

/**
 * Get a single issue by ID
 */
function getIssue(id) {
  const results = execQuery(`SELECT * FROM issue_overview_mv WHERE id = ?`, [id]);
  const issue = results[0] || null;
  if (issue) {
    issue.comments = getIssueComments(id);
  }
  return issue;
}

/**
 * Fetch discussion comments for an issue (bv-52 comments table, issue #187).
 * Returns [] when the bundle predates the comments table or the query fails,
 * so older exports keep working.
 */
function getIssueComments(id) {
  try {
    return execQuery(
      `SELECT author, text, created_at FROM comments WHERE issue_id = ? ORDER BY created_at ASC, id ASC`,
      [id]
    );
  } catch {
    return [];
  }
}

// ============================================================================
// Force-Graph View Data (Interactive Graph Visualization)
// ============================================================================

function parseLabelsJSON(labelsStr) {
  if (!labelsStr) return [];
  try {
    const parsed = JSON.parse(labelsStr);
    return Array.isArray(parsed) ? parsed : [];
  } catch {
    return [];
  }
}

function getGraphViewData() {
  // Pull from issue_overview_mv so the graph sees the same active-blocker
  // counts the issues view does — closing a blocker drops its dependents'
  // blocker_count to 0 here too, which getNodeColor uses to derive the
  // effective "blocked" colouring (bv-issue#143/#144).
  const issues = execQuery(`
    SELECT id, title, description, status, priority, issue_type, assignee, labels,
           created_at, updated_at, blocker_count, dependent_count
    FROM issue_overview_mv
  `).map(row => ({
    id: row.id,
    title: row.title || '',
    description: row.description || '',
    status: row.status || 'open',
    priority: row.priority ?? 2,
    type: row.issue_type || 'task',
    assignee: row.assignee || '',
    labels: parseLabelsJSON(row.labels),
    created_at: row.created_at,
    updated_at: row.updated_at,
    // Pre-computed active-blocker / active-dependent counts from the
    // materialized view (which excludes closed/tombstoned endpoints).
    blocker_count: row.blocker_count ?? 0,
    dependent_count: row.dependent_count ?? 0,
  }));

  const dependencies = execQuery(`
    SELECT issue_id, depends_on_id, type
    FROM dependencies
    WHERE type = 'blocks'
  `);

  return { issues, dependencies };
}

/**
 * Full-text search using FTS5 (if available)
 */
const BM25_WEIGHTS = '3.0, 2.0, 1.0, 1.5, 0.5'; // id, title, description, labels, assignee
const BM25_EXPR = `bm25(issues_fts, ${BM25_WEIGHTS})`;

function isLikelyIssueID(term) {
  return /^[A-Za-z]+-[A-Za-z0-9]+$/.test((term || '').trim());
}

function promoteExactID(term, rows) {
  const needle = (term || '').trim().toLowerCase();
  if (!needle || rows.length === 0) return rows;
  const idx = rows.findIndex(r => String(r.id || r.issue_id || '').toLowerCase() === needle);
  if (idx > 0) {
    const match = rows.splice(idx, 1)[0];
    rows.unshift(match);
  }
  return rows;
}

const SHORT_QUERY_TOKEN_LIMIT = 2;
const SHORT_QUERY_LENGTH_LIMIT = 12;
const SHORT_QUERY_MIN_TEXT_WEIGHT = 0.55;
const HYBRID_CANDIDATE_MIN = 200;
const HYBRID_CANDIDATE_MIN_SHORT = 300;

function countTokens(term) {
  if (!term) return 0;
  const matches = term.match(/[A-Za-z0-9]+/g);
  return matches ? matches.length : 0;
}

function isShortQuery(term) {
  const trimmed = (term || '').trim();
  if (!trimmed) return true;
  const tokens = countTokens(trimmed);
  return tokens <= SHORT_QUERY_TOKEN_LIMIT || trimmed.length <= SHORT_QUERY_LENGTH_LIMIT;
}

function adjustHybridWeightsForQuery(baseWeights, term) {
  if (!baseWeights || !isShortQuery(term)) return baseWeights;
  const currentText = Number(baseWeights.text ?? 0);
  if (currentText >= SHORT_QUERY_MIN_TEXT_WEIGHT) return baseWeights;
  const targetText = SHORT_QUERY_MIN_TEXT_WEIGHT;
  const remainder = (Number(baseWeights.pagerank ?? 0) +
    Number(baseWeights.status ?? 0) +
    Number(baseWeights.impact ?? 0) +
    Number(baseWeights.priority ?? 0) +
    Number(baseWeights.recency ?? 0));
  if (remainder <= 0 || targetText >= 1) {
    return {
      text: 1,
      pagerank: 0,
      status: 0,
      impact: 0,
      priority: 0,
      recency: 0,
    };
  }
  const scale = (1 - targetText) / remainder;
  return {
    text: targetText,
    pagerank: Number(baseWeights.pagerank ?? 0) * scale,
    status: Number(baseWeights.status ?? 0) * scale,
    impact: Number(baseWeights.impact ?? 0) * scale,
    priority: Number(baseWeights.priority ?? 0) * scale,
    recency: Number(baseWeights.recency ?? 0) * scale,
  };
}

function supportsFTS5() {
  if (DB_STATE.fts5 === null) {
    DB_STATE.fts5 = execQuery('PRAGMA module_list').some(row => row.name === 'fts5');
  }
  return DB_STATE.fts5;
}

function searchIssues(term, options = {}) {
  const {
    mode = 'text',
    preset = 'default',
    limit = 50,
    offset = 0,
    filters = {},
  } = options;

  const searchFilters = { ...filters };
  delete searchFilters.search;

  const { clauses, params } = buildFilterClauses(searchFilters, 'i');
  const baseSQL = `
      SELECT i.*,
             snippet(issues_fts, 2, '<mark>', '</mark>', '...', 32) as snippet,
             ${BM25_EXPR} as bm25_score
      FROM issues_fts
      JOIN issue_overview_mv i ON issues_fts.id = i.id
      WHERE issues_fts MATCH ?
    `;
  let sql = baseSQL;
  const queryParams = [term + '*'];
  if (clauses.length > 0) {
    sql += ` AND ${clauses.join(' AND ')}`;
    queryParams.push(...params);
  }

  let fetchLimit = limit;
  if (mode === 'hybrid') {
    const minCandidates = isShortQuery(term) ? HYBRID_CANDIDATE_MIN_SHORT : HYBRID_CANDIDATE_MIN;
    fetchLimit = Math.max(limit * 2, offset + limit, minCandidates);
  }
  const fetchOffset = mode === 'hybrid' ? 0 : offset;
  sql += ` ORDER BY ${BM25_EXPR} LIMIT ? OFFSET ?`;
  queryParams.push(fetchLimit, fetchOffset);

  let rows = [];
  if (supportsFTS5()) {
    try {
      rows = execQuery(sql, queryParams);
    } catch {
      // Invalid FTS syntax can still use plain substring matching. Continue
      // through the same hybrid scorer instead of silently losing ranking.
      rows = queryIssues({ ...searchFilters, search: term }, 'score', fetchLimit, fetchOffset);
    }
  } else {
    rows = queryIssues({ ...searchFilters, search: term }, 'score', fetchLimit, fetchOffset);
  }
  if (isLikelyIssueID(term)) {
    rows = promoteExactID(term, rows);
  }

  if (mode !== 'hybrid' || typeof HybridScorer === 'undefined' || typeof HYBRID_PRESETS === 'undefined') {
    return rows.slice(0, limit);
  }

  const maxBM25 = Math.max(...rows.map(r => Math.abs(r.bm25_score ?? 0)), 0);
  const normalized = rows.map(r => ({
    ...r,
    textScore: maxBM25 > 0 ? (1 - Math.min(Math.abs(r.bm25_score ?? 0) / maxBM25, 1)) : 0.5,
    blockerCount: r.blocker_count ?? r.blocked_by_count ?? 0,
    updatedAt: r.updated_at,
    pagerank: r.pagerank,
    status: r.status,
    priority: r.priority,
  }));

  const baseWeights = HYBRID_PRESETS[preset] || HYBRID_PRESETS.default;
  const weights = adjustHybridWeightsForQuery(baseWeights, term);
  let ranked = null;
  if (typeof window.scoreBatchHybrid === 'function') {
    ranked = window.scoreBatchHybrid(normalized, weights);
  }
  if (!Array.isArray(ranked)) {
    const scorer = new HybridScorer(weights);
    ranked = scorer.scoreAndRank(normalized);
  }
  if (isLikelyIssueID(term)) {
    ranked = promoteExactID(term, ranked);
  }

  return ranked
    .slice(offset, offset + limit)
    .map(r => ({
      ...r,
      text_score: r.textScore,
    }));
}

function countSearchIssues(term, filters = {}) {
  const searchFilters = { ...filters };
  delete searchFilters.search;
  if (!supportsFTS5()) return countIssues({ ...searchFilters, search: term });
  const { clauses, params } = buildFilterClauses(searchFilters, 'i');

  let sql = `
      SELECT COUNT(*) as count
      FROM issues_fts
      JOIN issue_overview_mv i ON issues_fts.id = i.id
      WHERE issues_fts MATCH ?
    `;
  const queryParams = [term + '*'];
  if (clauses.length > 0) {
    sql += ` AND ${clauses.join(' AND ')}`;
    queryParams.push(...params);
  }

  try {
    return execScalar(sql, queryParams) || 0;
  } catch {
    return countIssues({ ...searchFilters, search: term });
  }
}

/**
 * Get project statistics
 */
function getStats() {
  const stats = {};

  try {
    // Count by status
    const statusCounts = execQuery(`
      SELECT status, COUNT(*) as count
      FROM issue_overview_mv
      GROUP BY status
    `);
    console.log('[Stats] Status counts:', statusCounts);
    statusCounts.forEach(row => {
      stats[row.status] = row.count;
    });
    console.log('[Stats] Parsed stats:', stats);
  } catch (err) {
    console.error('[Stats] Error loading status counts:', err);
  }

  // Count blocked (has blocked_by_ids and status is open/in_progress)
  stats.blocked = execScalar(`
    SELECT COUNT(*) FROM issue_overview_mv
    WHERE blocked_by_ids IS NOT NULL
    AND blocked_by_ids <> ''
    AND status IN ('open', 'in_progress')
  `) || 0;

  // Count actionable (open/in_progress with NO open blockers)
  stats.actionable = execScalar(`
    SELECT COUNT(*) FROM issue_overview_mv
    WHERE status IN ('open', 'in_progress')
    AND (blocked_by_ids IS NULL OR blocked_by_ids = '')
  `) || 0;

  // Total
  stats.total = execScalar(`SELECT COUNT(*) FROM issue_overview_mv`) || 0;

  return stats;
}

/**
 * Get quick wins - actionable issues that unblock the most items
 */
function getQuickWins(limit = 5) {
  return execQuery(`
    SELECT * FROM issue_overview_mv
    WHERE status IN ('open', 'in_progress')
    AND (blocked_by_ids IS NULL OR blocked_by_ids = '')
    ORDER BY blocks_count DESC, triage_score DESC
    LIMIT ?
  `, [limit]);
}

/**
 * Get blockers to clear - issues blocking the most other issues
 */
function getBlockersToClose(limit = 5) {
  return execQuery(`
    SELECT * FROM issue_overview_mv
    WHERE status IN ('open', 'in_progress')
    AND blocks_count > 0
    ORDER BY blocks_count DESC, triage_score DESC
    LIMIT ?
  `, [limit]);
}

/**
 * Get distribution by type
 */
function getDistributionByType() {
	  return execQuery(`
	    SELECT issue_type as type, COUNT(*) as count
	    FROM issue_overview_mv
	    WHERE status <> 'closed'
	    GROUP BY issue_type
	    ORDER BY count DESC
	  `);
	}

/**
 * Get distribution by priority
 */
function getDistributionByPriority() {
	  return execQuery(`
	    SELECT priority, COUNT(*) as count
	    FROM issue_overview_mv
	    WHERE status <> 'closed'
	    GROUP BY priority
	    ORDER BY priority ASC
	  `);
	}

/**
 * Get top issues by triage score
 */
function getTopPicks(limit = 5) {
  return execQuery(`
    SELECT * FROM issue_overview_mv
    WHERE status IN ('open', 'in_progress')
    ORDER BY triage_score DESC
    LIMIT ?
  `, [limit]);
}

/**
 * Get recent issues by update time
 */
function getRecentIssues(limit = 10) {
  return execQuery(`
    SELECT * FROM issue_overview_mv
    ORDER BY updated_at DESC
    LIMIT ?
  `, [limit]);
}

/**
 * Get top issues by PageRank
 */
function getTopByPageRank(limit = 10) {
  return execQuery(`
    SELECT * FROM issue_overview_mv
    WHERE pagerank > 0
    ORDER BY pagerank DESC
    LIMIT ?
  `, [limit]);
}

/**
 * Get top issues by triage score
 */
function getTopByTriageScore(limit = 10) {
  return execQuery(`
    SELECT * FROM issue_overview_mv
    WHERE triage_score > 0
    ORDER BY triage_score DESC
    LIMIT ?
  `, [limit]);
}

/**
 * Get top blocking issues
 */
function getTopBlockers(limit = 10) {
  return execQuery(`
    SELECT * FROM issue_overview_mv
    WHERE blocks_count > 0
    ORDER BY blocks_count DESC
    LIMIT ?
  `, [limit]);
}

/**
 * Get top issues by betweenness centrality (bottlenecks)
 */
function getTopByBetweenness(limit = 10) {
  // Try betweenness column if it exists
  try {
    const results = execQuery(`
      SELECT * FROM issue_overview_mv
      WHERE betweenness > 0
      ORDER BY betweenness DESC
      LIMIT ?
    `, [limit]);
    if (results.length > 0) return results;
  } catch {
    // Column may not exist
  }

  // Fallback: use WASM if available
  if (GRAPH_STATE.ready) {
    const betweenness = GRAPH_STATE.graph.betweenness();
    if (betweenness && betweenness.length > 0) {
      // Get top N by betweenness value
      const indexed = Array.from(betweenness).map((val, idx) => ({ idx, val }));
      indexed.sort((a, b) => b.val - a.val);
      const topNodes = indexed.slice(0, limit);

      return topNodes.map(node => {
        const id = GRAPH_STATE.graph.nodeId(node.idx);
        const issue = getIssue(id);
        if (issue) {
          issue.betweenness = node.val;
        }
        return issue;
      }).filter(Boolean);
    }
  }

  return [];
}

/**
 * Get top issues by critical path depth (keystones)
 */
function getTopByCriticalPath(limit = 10) {
  // Try critical_path_depth column if it exists
  try {
    const results = execQuery(`
      SELECT * FROM issue_overview_mv
      WHERE critical_path_depth > 0
      ORDER BY critical_path_depth DESC
      LIMIT ?
    `, [limit]);
    if (results.length > 0) return results;
  } catch {
    // Column may not exist
  }

  // Fallback: use WASM if available
  if (GRAPH_STATE.ready) {
    const heights = GRAPH_STATE.graph.criticalPathHeights();
    if (heights && heights.length > 0) {
      const indexed = Array.from(heights).map((val, idx) => ({ idx, val }));
      indexed.sort((a, b) => b.val - a.val);
      const topNodes = indexed.slice(0, limit);

      return topNodes.map(node => {
        const id = GRAPH_STATE.graph.nodeId(node.idx);
        const issue = getIssue(id);
        if (issue) {
          issue.critical_path_depth = node.val;
        }
        return issue;
      }).filter(Boolean);
    }
  }

  return [];
}

/**
 * Get top issues by HITS hub score (nodes that point to many authorities)
 * Hub nodes are connectors that reference many important items.
 */
function getTopByHITSHub(limit = 10) {
  if (!GRAPH_STATE.ready) return [];

  try {
    const hitsResult = GRAPH_STATE.graph.hitsDefault();
    if (!hitsResult || !hitsResult.hub) return [];

    const hubScores = Array.from(hitsResult.hub);
    const indexed = hubScores.map((val, idx) => ({ idx, val }));
    indexed.sort((a, b) => b.val - a.val);
    const topNodes = indexed.slice(0, limit);

    return topNodes.map(node => {
      const id = GRAPH_STATE.graph.nodeId(node.idx);
      const issue = getIssue(id);
      if (issue) {
        issue.hits_hub = node.val;
      }
      return issue;
    }).filter(Boolean);
  } catch (e) {
    console.warn('[viewer] getTopByHITSHub failed:', e);
    return [];
  }
}

/**
 * Get top issues by HITS authority score (nodes pointed to by many hubs)
 * Authority nodes are important endpoints that many connectors reference.
 */
function getTopByHITSAuth(limit = 10) {
  if (!GRAPH_STATE.ready) return [];

  try {
    const hitsResult = GRAPH_STATE.graph.hitsDefault();
    if (!hitsResult || !hitsResult.authority) return [];

    const authScores = Array.from(hitsResult.authority);
    const indexed = authScores.map((val, idx) => ({ idx, val }));
    indexed.sort((a, b) => b.val - a.val);
    const topNodes = indexed.slice(0, limit);

    return topNodes.map(node => {
      const id = GRAPH_STATE.graph.nodeId(node.idx);
      const issue = getIssue(id);
      if (issue) {
        issue.hits_auth = node.val;
      }
      return issue;
    }).filter(Boolean);
  } catch (e) {
    console.warn('[viewer] getTopByHITSAuth failed:', e);
    return [];
  }
}

/**
 * Get top issues by k-core number (densely connected groups)
 * Higher k-core = more tightly coupled with other issues.
 */
function getTopByKCore(limit = 10) {
  if (!GRAPH_STATE.ready) return [];

  try {
    const kcoreValues = GRAPH_STATE.graph.kcore();
    if (!kcoreValues || kcoreValues.length === 0) return [];

    const indexed = Array.from(kcoreValues).map((val, idx) => ({ idx, val }));
    indexed.sort((a, b) => b.val - a.val);
    const topNodes = indexed.slice(0, limit);

    return topNodes.map(node => {
      const id = GRAPH_STATE.graph.nodeId(node.idx);
      const issue = getIssue(id);
      if (issue) {
        issue.kcore = node.val;
      }
      return issue;
    }).filter(Boolean);
  } catch (e) {
    console.warn('[viewer] getTopByKCore failed:', e);
    return [];
  }
}

/**
 * Get articulation points (cut vertices) - removing these disconnects the graph.
 * Critical structural nodes whose removal would fragment the project.
 */
function getArticulationPoints() {
  if (!GRAPH_STATE.ready) return [];

  try {
    const artPoints = GRAPH_STATE.graph.articulationPoints();
    if (!artPoints || artPoints.length === 0) return [];

    return Array.from(artPoints).map(idx => {
      const id = GRAPH_STATE.graph.nodeId(idx);
      const issue = getIssue(id);
      if (issue) {
        issue.is_articulation = true;
      }
      return issue;
    }).filter(Boolean);
  } catch (e) {
    console.warn('[viewer] getArticulationPoints failed:', e);
    return [];
  }
}

/**
 * Get issues sorted by slack (schedule flexibility)
 * Issues with 0 slack are on the critical path (no room for delay).
 */
function getIssuesBySlack(limit = 10, showZeroSlack = true) {
  if (!GRAPH_STATE.ready) return [];

  try {
    const slackValues = GRAPH_STATE.graph.slack();
    if (!slackValues || slackValues.length === 0) return [];

    const indexed = Array.from(slackValues).map((val, idx) => ({ idx, val }));

    if (showZeroSlack) {
      // Show critical path items (zero slack)
      const criticalPath = indexed.filter(item => item.val === 0);
      return criticalPath.slice(0, limit).map(node => {
        const id = GRAPH_STATE.graph.nodeId(node.idx);
        const issue = getIssue(id);
        if (issue) {
          issue.slack = 0;
          issue.on_critical_path = true;
        }
        return issue;
      }).filter(Boolean);
    } else {
      // Show items with most slack (most flexible scheduling)
      indexed.sort((a, b) => b.val - a.val);
      return indexed.slice(0, limit).map(node => {
        const id = GRAPH_STATE.graph.nodeId(node.idx);
        const issue = getIssue(id);
        if (issue) {
          issue.slack = node.val;
          issue.on_critical_path = node.val === 0;
        }
        return issue;
      }).filter(Boolean);
    }
  } catch (e) {
    console.warn('[viewer] getIssuesBySlack failed:', e);
    return [];
  }
}

/**
 * Get cycle information from graph
 */
function getCycleInfo() {
  if (!GRAPH_STATE.ready) {
    return { hasCycles: false, cycleCount: 0, suggestions: null };
  }

  const hasCycles = GRAPH_STATE.graph.hasCycles();
  const suggestions = hasCycles ? getCycleBreakSuggestions(5) : null;

  return {
    hasCycles,
    cycleCount: suggestions?.cycles?.length || 0,
    suggestions,
  };
}

/**
 * Get the full critical path sequence from root to sink.
 * Returns array of issue IDs representing the longest dependency chain.
 */
function getCriticalPathSequence() {
  if (!GRAPH_STATE.ready) return null;

  const graph = GRAPH_STATE.graph;
  const heights = graph.criticalPathHeights();
  if (!heights || heights.length === 0) return null;

  const heightsArray = Array.from(heights);
  const maxHeight = Math.max(...heightsArray);
  if (maxHeight === 0) return null; // Cyclic graph or empty

  // Find sink node(s) with max height
  let sinkIdx = -1;
  heightsArray.forEach((h, idx) => {
    if (h === maxHeight) sinkIdx = idx;
  });

  if (sinkIdx === -1) return null;

  // Backtrack from sink to root to reconstruct the critical path
  const path = [];
  let currentIdx = sinkIdx;

  while (currentIdx !== -1) {
    const issueId = graph.nodeId(currentIdx);
    if (issueId) path.unshift(issueId);

    // Find predecessor with height = current height - 1
    const predecessors = graph.predecessors(currentIdx);
    if (!predecessors || predecessors.length === 0) break;

    const predArray = Array.from(predecessors);
    const currentHeight = heightsArray[currentIdx];
    let nextIdx = -1;

    for (const predIdx of predArray) {
      if (Math.abs(heightsArray[predIdx] - (currentHeight - 1)) < 0.001) {
        nextIdx = predIdx;
        break;
      }
    }

    currentIdx = nextIdx;
  }

  return {
    path,
    length: path.length,
    maxHeight,
  };
}

/**
 * Get export metadata
 */
function getMeta() {
  const meta = {};
  const rows = execQuery(`SELECT key, value FROM export_meta`);
  rows.forEach(row => {
    meta[row.key] = row.value;
  });
  return meta;
}

/**
 * Get dependencies for an issue
 */
function getIssueDependencies(id) {
  const blocks = execQuery(`
    SELECT i.* FROM issue_overview_mv i
    JOIN dependencies d ON i.id = d.depends_on_id
    WHERE d.issue_id = ? AND d.type = 'blocks'
  `, [id]);

  const blockedBy = execQuery(`
    SELECT i.* FROM issue_overview_mv i
    JOIN dependencies d ON i.id = d.issue_id
    WHERE d.depends_on_id = ? AND d.type = 'blocks'
  `, [id]);

  return { blocks, blockedBy };
}

// ============================================================================
// URL State Sync - Shareable filtered views
// ============================================================================

/**
 * Serialize filters to URL search params
 */
function filtersToURL(filters, sort, searchQuery) {
  const params = new URLSearchParams();

  if (filters.status?.length) {
    const statuses = Array.isArray(filters.status) ? filters.status : [filters.status];
    if (statuses.length > 0 && statuses[0]) {
      params.set('status', statuses.join(','));
    }
  }

  if (filters.type?.length) {
    const types = Array.isArray(filters.type) ? filters.type : [filters.type];
    if (types.length > 0 && types[0]) {
      params.set('type', types.join(','));
    }
  }

  if (filters.priority?.length) {
    const priorities = Array.isArray(filters.priority) ? filters.priority : [filters.priority];
    const validPriorities = priorities.filter(p => p !== '' && p !== null && p !== undefined);
    if (validPriorities.length > 0) {
      params.set('priority', validPriorities.join(','));
    }
  }

  if (filters.labels?.length) {
    params.set('labels', filters.labels.join(','));
  }

  if (filters.assignee) {
    params.set('assignee', filters.assignee);
  }

  if (filters.hasBlockers === true || filters.hasBlockers === 'true') {
    params.set('blocked', 'true');
  } else if (filters.hasBlockers === false || filters.hasBlockers === 'false') {
    params.set('blocked', 'false');
  }

  if (filters.isBlocking === true || filters.isBlocking === 'true') {
    params.set('blocking', 'true');
  }

  if (searchQuery) {
    params.set('q', searchQuery);
  }

  if (sort && sort !== 'priority') {
    params.set('sort', sort);
  }

  return params.toString();
}

/**
 * Parse URL search params to filters
 */
function filtersFromURL() {
  const hash = window.location.hash;
  const queryIndex = hash.indexOf('?');
  if (queryIndex === -1) return { filters: {}, sort: 'priority', searchQuery: '' };

  const params = new URLSearchParams(hash.slice(queryIndex + 1));

  const filters = {};

  const statusParam = params.get('status');
  if (statusParam) {
    filters.status = statusParam.split(',').filter(Boolean);
  }

  const typeParam = params.get('type');
  if (typeParam) {
    filters.type = typeParam.split(',').filter(Boolean);
  }

  const priorityParam = params.get('priority');
  if (priorityParam) {
    filters.priority = priorityParam.split(',').map(Number).filter(n => !isNaN(n));
  }

  const labelsParam = params.get('labels');
  if (labelsParam) {
    filters.labels = labelsParam.split(',').filter(Boolean);
  }

  const assigneeParam = params.get('assignee');
  if (assigneeParam) {
    filters.assignee = assigneeParam;
  }

  const blockedParam = params.get('blocked');
  if (blockedParam === 'true') {
    filters.hasBlockers = true;
  } else if (blockedParam === 'false') {
    filters.hasBlockers = false;
  }

  const blockingParam = params.get('blocking');
  if (blockingParam === 'true') {
    filters.isBlocking = true;
  }

  return {
    filters,
    sort: params.get('sort') || 'priority',
    searchQuery: params.get('q') || '',
  };
}

/**
 * Update URL with current filter state (without page reload)
 */
function syncFiltersToURL(view, filters, sort, searchQuery) {
  if (window.location.hash.startsWith('#/issue/')) return;
  const paramString = filtersToURL(filters, sort, searchQuery);
  const baseHash = `#/${view}`;
  const newHash = paramString ? `${baseHash}?${paramString}` : baseHash;

  if (window.location.hash !== newHash) {
    history.replaceState(null, '', newHash);
  }
}

// ============================================================================
// Router - Hash-based SPA navigation
// ============================================================================

/**
 * Route definitions with pattern matching
 * :param syntax for dynamic segments
 */
const ROUTES = [
  { pattern: '/', view: 'dashboard' },
  { pattern: '/issues', view: 'issues' },
  { pattern: '/issue/:id', view: 'issue' },
  { pattern: '/insights', view: 'insights' },
  { pattern: '/graph', view: 'graph' },
];

const DIRECT_VIEW_ROUTES = new Set(['issues', 'insights', 'graph']);

/**
 * Resolve a static view from the final URL path segment.
 *
 * Static hosts can rewrite paths such as /project/graph to index.html. The
 * viewer keeps hash routing for in-app navigation, but honors that clean path
 * on the initial load when no hash route is present.
 */
function routeHashFromPathname(pathname) {
  const segments = (pathname || '/').split('/').filter(Boolean);
  const finalSegment = segments[segments.length - 1] || '';

  if (DIRECT_VIEW_ROUTES.has(finalSegment)) {
    return `#/${finalSegment}`;
  }

  return '#/';
}

/**
 * Parse hash into view and params
 */
function parseRoute(hash) {
  // Remove leading # and extract path vs query
  const hashContent = hash.slice(1) || '/';
  const [path, query] = hashContent.split('?');
  const normalizedPath = path.startsWith('/') ? path : '/' + path;

  // Try to match each route pattern
  for (const route of ROUTES) {
    const match = matchPattern(route.pattern, normalizedPath);
    if (match) {
      return {
        view: route.view,
        params: match.params,
        query: query ? new URLSearchParams(query) : new URLSearchParams(),
      };
    }
  }

  // Default to dashboard
  return { view: 'dashboard', params: {}, query: new URLSearchParams() };
}

/**
 * Match a URL path against a pattern with :param placeholders
 */
function matchPattern(pattern, path) {
  const patternParts = pattern.split('/').filter(Boolean);
  const pathParts = path.split('/').filter(Boolean);

  // Handle root route
  if (patternParts.length === 0 && pathParts.length === 0) {
    return { params: {} };
  }

  if (patternParts.length !== pathParts.length) {
    return null;
  }

  const params = {};
  for (let i = 0; i < patternParts.length; i++) {
    const patternPart = patternParts[i];
    const pathPart = pathParts[i];

    if (patternPart.startsWith(':')) {
      // Dynamic segment - capture as param
      params[patternPart.slice(1)] = decodeURIComponent(pathPart);
    } else if (patternPart !== pathPart) {
      // Static segment mismatch
      return null;
    }
  }

  return { params };
}

/**
 * Navigate to a route (pushes to history)
 */
function navigate(path) {
  const newHash = path.startsWith('#') ? path : '#' + path;
  if (window.location.hash !== newHash) {
    window.location.hash = newHash;
  }
}

/**
 * Navigate to issue detail
 */
function navigateToIssue(id) {
  navigate(`/issue/${encodeURIComponent(id)}`);
}

/**
 * Navigate to issues list with filters
 */
function navigateToIssues(filters = {}, sort = 'priority', search = '') {
  const params = filtersToURL(filters, sort, search);
  navigate(`/issues${params ? '?' + params : ''}`);
}

/**
 * Navigate to dashboard
 */
function navigateToDashboard() {
  navigate('/');
}

/**
 * Go back in history, or to a fallback
 */
function goBack(fallback = '/') {
  if (window.history.length > 1) {
    window.history.back();
  } else {
    navigate(fallback);
  }
}

// ============================================================================
// Alpine.js Application
// ============================================================================

/**
 * Format ISO date to readable string with relative time for recent dates
 */
function formatDate(isoString) {
  if (!isoString) return '';
  try {
    const date = new Date(isoString);
    if (isNaN(date.getTime())) return isoString; // Invalid date

    const now = new Date();
    const diffMs = now - date;

    // Future dates or very recent: show absolute date
    if (diffMs < 0) {
      return date.toLocaleDateString('en-US', {
        year: 'numeric',
        month: 'short',
        day: 'numeric',
      });
    }

    const diffSecs = Math.floor(diffMs / 1000);
    const diffMins = Math.floor(diffSecs / 60);
    const diffHours = Math.floor(diffMins / 60);
    const diffDays = Math.floor(diffHours / 24);

    // Relative time for recent dates (< 7 days)
    if (diffSecs < 60) return 'just now';
    if (diffMins < 60) return `${diffMins}m ago`;
    if (diffHours < 24) return `${diffHours}h ago`;
    if (diffDays === 1) return 'yesterday';
    if (diffDays < 7) return `${diffDays}d ago`;

    // Absolute date for older items
    return date.toLocaleDateString('en-US', {
      year: 'numeric',
      month: 'short',
      day: 'numeric',
    });
  } catch {
    return isoString;
  }
}

/**
 * Format ISO date to full readable string (always absolute, with time)
 */
function formatDateFull(isoString) {
  if (!isoString) return '';
  try {
    const date = new Date(isoString);
    if (isNaN(date.getTime())) return isoString; // Invalid date
    return date.toLocaleDateString('en-US', {
      year: 'numeric',
      month: 'short',
      day: 'numeric',
      hour: '2-digit',
      minute: '2-digit',
    });
  } catch {
    return isoString;
  }
}

/**
 * Safely format a number, returning em-dash for NaN/undefined/null/Infinity
 * @param {number} value - The number to format
 * @param {number} decimals - Number of decimal places (default 2)
 * @param {string} suffix - Optional suffix like '%' (default '')
 * @returns {string} Formatted number or em-dash
 */
function safeNum(value, decimals = 2, suffix = '') {
  if (value === undefined || value === null || typeof value !== 'number' || !isFinite(value)) {
    return '—';
  }
  return value.toFixed(decimals) + suffix;
}

/**
 * Calculate score breakdown as percentages for visualization
 * Returns array of { name, value, percent, color, weight } sorted by contribution
 */
function getScoreBreakdownBars(breakdown) {
  if (!breakdown) return [];

  const components = [
    { name: 'PageRank', key: 'pagerank', color: '#3b82f6', weight: 0.22 },
    { name: 'Betweenness', key: 'betweenness', color: '#f97316', weight: 0.20 },
    { name: 'Blocker Ratio', key: 'blocker_ratio', color: '#ef4444', weight: 0.13 },
    { name: 'Priority', key: 'priority_boost', color: '#8b5cf6', weight: 0.10 },
    { name: 'Time Impact', key: 'time_to_impact', color: '#06b6d4', weight: 0.10 },
    { name: 'Urgency', key: 'urgency', color: '#ec4899', weight: 0.10 },
    { name: 'Risk', key: 'risk', color: '#f59e0b', weight: 0.10 },
    { name: 'Staleness', key: 'staleness', color: '#6b7280', weight: 0.05 },
  ];

  const total = components.reduce((sum, c) => sum + (breakdown[c.key] || 0), 0);

  return components
    .map(c => ({
      name: c.name,
      value: breakdown[c.key] || 0,
      percent: total > 0 ? ((breakdown[c.key] || 0) / total * 100) : 0,
      color: c.color,
      weight: c.weight,
      // Normalized value for bar width (0-100)
      normalized: (breakdown[c.key + '_norm'] || 0) * 100
    }))
    .filter(c => c.value > 0)
    .sort((a, b) => b.value - a.value);
}

/**
 * Format JSON with syntax highlighting for display
 */
function formatJsonWithHighlight(obj) {
  if (!obj) return '';
  try {
    const json = JSON.stringify(obj, null, 2);
    // Add syntax highlighting classes
    return json
      .replace(/"([^"]+)":/g, '<span class="text-purple-600 dark:text-purple-400">"$1"</span>:')
      .replace(/: "([^"]*)"/g, ': <span class="text-green-600 dark:text-green-400">"$1"</span>')
      .replace(/: (\d+\.?\d*)/g, ': <span class="text-blue-600 dark:text-blue-400">$1</span>')
      .replace(/: (true|false)/g, ': <span class="text-amber-600 dark:text-amber-400">$1</span>')
      .replace(/: (null)/g, ': <span class="text-gray-500 dark:text-gray-400">$1</span>');
  } catch {
    return String(obj);
  }
}

/**
 * Render markdown safely
 */
function renderMarkdown(text) {
  if (!text) return '';
  try {
    const html = marked.parse(text);
    return DOMPurify.sanitize(html);
  } catch {
    return DOMPurify.sanitize(text);
  }
}

/**
 * Render markdown as inline HTML (no block elements) for excerpts
 * Converts markdown to HTML but wraps in a span to work with line-clamp
 */
function renderMarkdownInline(text) {
  if (!text) return '';
  try {
    // Use marked's parseInline to avoid block elements like <p>
    const html = marked.parseInline(text);
    return DOMPurify.sanitize(html);
  } catch {
    return DOMPurify.sanitize(text);
  }
}

/**
 * Strip markdown formatting to plain text for excerpts
 * Preserves readable text content without markdown syntax
 */
function stripMarkdownToText(text) {
  if (!text) return '';
  try {
    // Remove common markdown syntax while preserving text
    return text
      // Remove code blocks (```...```)
      .replace(/```[\s\S]*?```/g, ' ')
      // Remove inline code (`...`)
      .replace(/`([^`]+)`/g, '$1')
      // Remove images ![alt](url)
      .replace(/!\[([^\]]*)\]\([^)]+\)/g, '$1')
      // Convert links [text](url) to just text
      .replace(/\[([^\]]+)\]\([^)]+\)/g, '$1')
      // Remove bold/italic (**, *, __, _)
      .replace(/(\*\*|__)(.*?)\1/g, '$2')
      .replace(/(\*|_)(.*?)\1/g, '$2')
      // Remove headers (# ## ###)
      .replace(/^#{1,6}\s+/gm, '')
      // Remove blockquotes
      .replace(/^>\s+/gm, '')
      // Remove horizontal rules
      .replace(/^[-*_]{3,}\s*$/gm, '')
      // Remove list markers
      .replace(/^[\s]*[-*+]\s+/gm, '')
      .replace(/^[\s]*\d+\.\s+/gm, '')
      // Collapse multiple whitespace/newlines
      .replace(/\s+/g, ' ')
      .trim();
  } catch {
    return text;
  }
}

/**
 * Main Alpine.js application component
 */
function beadsApp() {
  return {
    // State
    loading: true,
    loadingMessage: 'Initializing...',
    error: null,
    globalError: null,       // Modal error from ERROR_STATE
    showDiagnostics: false,  // Toggle for diagnostics panel (press 'd')
    diagnostics: DIAGNOSTICS, // Reference to global diagnostics
    wasmStatus: WASM_STATUS,  // WASM support status
    toasts: [],              // Toast notifications
    view: 'dashboard',
    mobileMenuOpen: false,   // Mobile hamburger menu state
    mobileSearchOpen: false, // Mobile search bar state
    filtersExpanded: false,  // Collapsible filters on mobile
    darkMode: localStorage.getItem('darkMode') !== null
      ? localStorage.getItem('darkMode') === 'true'
      : true, // Default to dark mode

    // Data
    stats: {},
    meta: {},
    dbSource: 'loading',
    searchBackend: '',

    // Issues list
    issues: [],
    totalIssues: 0,
    page: 1,
    pageSize: 20,

    // Filter options (populated from database)
    filterOptions: {
      statuses: [],
      types: [],
      priorities: [],
      assignees: [],
      labels: [],
    },

    // Filters (supports multi-select arrays)
    filters: {
      status: [],      // Array for multi-select
      type: [],        // Array for multi-select
      priority: [],    // Array for multi-select
      labels: [],      // Array for multi-select
      assignee: '',    // Single select
      hasBlockers: null, // true/false/null
      isBlocking: null,  // true/false/null
    },
    sort: 'priority',
    searchQuery: '',
    searchMode: 'text',
    searchPreset: 'default',

    // Dashboard data
    topPicks: [],
    recentIssues: [],
    topByPageRank: [],
    topByTriageScore: [],
    topBlockers: [],
    quickWins: [],
    blockersToClose: [],
    distributionByType: [],
    distributionByPriority: [],

    // Selected issue
    selectedIssue: null,
    copyLinkMessage: '',
    showDepGraph: false,
    issueNavList: [], // List of issue IDs for j/k navigation
    showKeyboardHelp: false, // Keyboard shortcuts help modal

    // Graph engine state
    graphReady: false,
    graphMetrics: null,
    whatIfResult: null,
    topKSet: null,

    // Force-graph view (interactive dependency visualization)
    forceGraphReady: false,
    forceGraphLoading: false,
    forceGraphError: null,
    forceGraphModule: null,
    graphDetailNode: null, // Currently selected node for detail pane

    // Graph loading stages: 'init' | 'loading-data' | 'computing-metrics' | 'simulating' | null
    graphLoadingStage: null,
    // Graph simulation progress (0-100, null = not simulating)
    graphSimulationProgress: null,
    graphSimulationDone: false,

    // Heatmap & metrics mode
    graphHeatmapActive: false,
    graphSizeMetric: 'pagerank', // pagerank | betweenness | critical | indegree

    // Critical path highlighting
    showCriticalPath: false,
    criticalPathData: null, // { path: [issueIds], length: number, animating: boolean }
    criticalPathAnimationStep: -1,

    // Insights view data
    topByBetweenness: [],
    topByCriticalPath: [],
    topByHITSHub: [],
    topByHITSAuth: [],
    topByKCore: [],
    articulationPoints: [],
    criticalPathSlack: [], // Issues with 0 slack (on critical path)
    cycleInfo: null,
    topImpactIssues: [],

    // Full triage data from triage.json (robot mode output)
    triageData: null,
    showTriageJson: false, // Modal for raw JSON view

    /**
     * Initialize the application
     */
    async init() {
      // Apply dark mode
      if (this.darkMode) {
        document.documentElement.classList.add('dark');
      }

      // Body scroll lock for modals (iOS scroll bleed fix)
      // Watches both issue modal and graph detail pane
      const updateBodyScrollLock = () => {
        const hasModal = !!(this.selectedIssue || this.graphDetailNode);
        if (hasModal) {
          // Save scroll position before locking
          document.body.style.setProperty('--scroll-y', `${window.scrollY}px`);
          document.body.classList.add('modal-open');
        } else {
          // Restore scroll position after unlocking
          const scrollY = document.body.style.getPropertyValue('--scroll-y');
          document.body.classList.remove('modal-open');
          if (scrollY) {
            window.scrollTo(0, parseInt(scrollY || '0', 10));
          }
        }
      };

      // Watch for modal state changes using Alpine's $watch
      this.$watch('selectedIssue', updateBodyScrollLock);
      this.$watch('graphDetailNode', updateBodyScrollLock);

      // Scroll to top on view change (respect reduced motion preference)
      this.$watch('view', (newView, oldView) => {
        if (newView !== oldView && !this.selectedIssue && !this.graphDetailNode) {
          const prefersReducedMotion = window.matchMedia('(prefers-reduced-motion: reduce)').matches;
          window.scrollTo({ top: 0, behavior: prefersReducedMotion ? 'auto' : 'smooth' });
        }
      });

      // Listen for system preference changes (only if no stored preference)
      window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', (e) => {
        if (localStorage.getItem('darkMode') === null) {
          this.darkMode = e.matches;
          document.documentElement.classList.toggle('dark', this.darkMode);
          // Re-render Mermaid graphs if visible
          if (this.showDepGraph && this.selectedIssue) {
            this.renderDepGraph();
          }
        }
      });

      // Listen for toast events
      window.addEventListener('show-toast', (e) => {
        // Validate toast data to prevent template errors
        const toast = e.detail;
        if (!toast || typeof toast.message !== 'string') {
          console.warn('[Toast] Invalid toast data:', toast);
          return;
        }
        // Ensure required properties
        const validToast = {
          id: toast.id || Date.now(),
          message: toast.message,
          type: toast.type || 'info'
        };
        this.toasts.push(validToast);
        setTimeout(() => {
          this.toasts = this.toasts.filter(t => t.id !== validToast.id);
        }, 5000);
      });

      // Listen for keyboard shortcuts (vim-style navigation)
      window.addEventListener('keydown', (e) => {
        // Skip if typing in input fields
        const isInput = ['INPUT', 'TEXTAREA'].includes(e.target.tagName);

        // '/' focuses search (works globally)
        if (e.key === '/' && !isInput) {
          e.preventDefault();
          const searchInput = document.querySelector('input[x-model="searchQuery"]');
          if (searchInput) {
            searchInput.focus();
            searchInput.select();
          }
          return;
        }

        // '?' shows keyboard help
        if (e.key === '?' && !isInput) {
          e.preventDefault();
          this.showKeyboardHelp = true;
          return;
        }

        // 'd' toggles diagnostics panel
        if (e.key === 'd' && !isInput) {
          this.showDiagnostics = !this.showDiagnostics;
          return;
        }

        // 'c' toggles critical path highlighting
        if (e.key === 'c' && !isInput) {
          this.toggleCriticalPath();
          return;
        }

        // 'h' navigates to first blocker (blocked-by) - when issue modal open
        if (e.key === 'h' && !isInput && this.selectedIssue) {
          const deps = getIssueDependencies(this.selectedIssue.id);
          if (deps && deps.blockedBy && deps.blockedBy.length > 0) {
            this.selectIssue(deps.blockedBy[0].id);
          }
          return;
        }

        // 'l' navigates to first dependent (blocks) - when issue modal open
        if (e.key === 'l' && !isInput && this.selectedIssue) {
          const deps = getIssueDependencies(this.selectedIssue.id);
          if (deps && deps.blocks && deps.blocks.length > 0) {
            this.selectIssue(deps.blocks[0].id);
          }
          return;
        }

        // 'o' opens issue detail in list view (when row is focused)
        if (e.key === 'o' && !isInput && this.view === 'issues' && !this.selectedIssue) {
          // Focus the first issue if none selected
          if (this.issues.length > 0) {
            this.selectIssue(this.issues[0].id);
          }
          return;
        }
      });

      // Force-graph integration: double-click opens the full issue modal.
      // Single click pins the node in the side detail pane (handled by the
      // listener registered later inside loadGraph). Detect double-click via a
      // manual timer so we don't depend on `event.detail` (which some
      // force-graph builds and synthetic events do not populate reliably —
      // bv-142).
      const DBLCLICK_MS = 350;
      let _lastNodeClick = { id: null, t: 0 };
      document.addEventListener('bv-graph:nodeClick', (e) => {
        const nodeId = e?.detail?.node?.id;
        const ev = e?.detail?.event;
        if (!nodeId) return;

        // Let graph interactions work:
        // - Shift+click triggers what-if
        // - Ctrl/Meta+click highlights dependency paths
        if (ev && (ev.shiftKey || ev.ctrlKey || ev.metaKey)) {
          _lastNodeClick = { id: null, t: 0 };
          return;
        }

        const now = Date.now();
        const isDouble =
          _lastNodeClick.id === nodeId &&
          now - _lastNodeClick.t < DBLCLICK_MS;

        if (isDouble) {
          _lastNodeClick = { id: null, t: 0 };
          this.selectIssue(nodeId);
        } else {
          _lastNodeClick = { id: nodeId, t: now };
        }
      });

      try {
        this.loadingMessage = 'Loading sql.js WebAssembly...';
        await loadDatabase((msg) => {
          this.loadingMessage = msg;
        });

        // Check for global errors that may have occurred during loading
        if (ERROR_STATE.error) {
          this.globalError = ERROR_STATE.error;
          this.loading = false;
          return;
        }

        this.dbSource = DB_STATE.source;
        this.searchBackend = supportsFTS5() ? 'FTS5' : 'substring';
        this.loadingMessage = 'Loading data...';

        // Load initial data
        this.meta = getMeta();
        this.stats = getStats();
        DIAGNOSTICS.issueCount = this.stats.total || 0;
        if (typeof window.initHybridWasmScorer === 'function') {
          window.initHybridWasmScorer(DIAGNOSTICS.issueCount)
            .then((enabled) => {
              DIAGNOSTICS.hybridWasm = !!enabled;
              if (!enabled && typeof window.getHybridWasmStatus === 'function') {
                DIAGNOSTICS.hybridWasmReason = window.getHybridWasmStatus().reason;
              }
            })
            .catch((err) => {
              DIAGNOSTICS.hybridWasm = false;
              DIAGNOSTICS.hybridWasmReason = err?.message || 'Hybrid WASM init failed';
            });
        }

        this.topPicks = getTopPicks(5);
        this.recentIssues = getRecentIssues(10);
        this.topByPageRank = getTopByPageRank(10);
        this.topByTriageScore = getTopByTriageScore(10);
        this.topBlockers = getTopBlockers(10);

        // Dashboard data
        this.quickWins = getQuickWins(5);
        this.blockersToClose = getBlockersToClose(5);
        this.distributionByType = getDistributionByType();
        this.distributionByPriority = getDistributionByPriority();

        // Load filter options for dropdowns
        this.filterOptions = getFilterOptions();

        // Load issues for list view (initial data)
        this.loadIssues();

        // Initialize WASM graph engine (non-blocking)
        this.loadingMessage = 'Loading graph engine...';
        this.graphReady = await initGraphEngine();
        DIAGNOSTICS.graphWasm = this.graphReady;
        if (this.graphReady) {
          this.topKSet = getTopKSet(5);
          this.topByBetweenness = getTopByBetweenness(10);
          this.topByCriticalPath = getTopByCriticalPath(10);
          this.cycleInfo = getCycleInfo();
          this.topImpactIssues = topWhatIf(10);
          // Additional TUI-style metrics
          this.topByHITSHub = getTopByHITSHub(10);
          this.topByHITSAuth = getTopByHITSAuth(10);
          this.topByKCore = getTopByKCore(10);
          this.articulationPoints = getArticulationPoints();
          this.criticalPathSlack = getIssuesBySlack(10, true); // Zero slack = critical path
        }

        // Handle the initial hash route or a host-rewritten clean path after
        // the graph engine is ready, so direct graph entry cannot start a
        // second concurrent WASM initialization.
        this.handleHashChange();

        // Listen for hash changes (browser back/forward)
        window.addEventListener('hashchange', () => this.handleHashChange());

        // Record load time
        DIAGNOSTICS.loadTimeMs = Date.now() - DIAGNOSTICS.startTime;

        // Initialize charts dashboard (bv-wb6h)
        if (typeof window.bvCharts !== 'undefined') {
          try {
            const graphData = getGraphViewData();
            window.bvCharts.init(graphData.issues, graphData.dependencies);
          } catch (e) {
            console.warn('[Charts] Init failed:', e);
          }
        }

        // Load full triage data for insights view
        // Use cache-busting to avoid stale data from CDN
        try {
          const triageResp = await fetch(`./data/triage.json?_t=${Date.now()}`);
          if (triageResp.ok) {
            this.triageData = await triageResp.json();
            console.log('[Viewer] Triage data loaded:', this.triageData?.meta?.issue_count, 'issues');
          }
        } catch (triageErr) {
          console.log('[Viewer] No triage.json found (optional for insights)');
        }

        this.loading = false;
      } catch (err) {
        console.error('Init failed:', err);
        this.error = err.message || 'Failed to load database';
        // Sync global error state
        if (ERROR_STATE.error) {
          this.globalError = ERROR_STATE.error;
        }
        this.loading = false;
      }
    },

    async copyIssueLink() {
      this.copyLinkMessage = '';
      if (!this.selectedIssue) return;
      const url = new URL(window.location.href);
      url.hash = '#/issue/' + encodeURIComponent(this.selectedIssue.id);
      try {
        await navigator.clipboard.writeText(url.href);
        this.copyLinkMessage = 'Link copied';
      } catch (err) {
        this.copyLinkMessage = 'Could not copy. Select the issue link to copy it manually.';
      }
    },

    /**
     * Handle hash change (browser back/forward navigation)
     */
    handleHashChange() {
      this.copyLinkMessage = '';
      const urlState = filtersFromURL();
      const hash = window.location.hash;

      // Parse route
      const route = parseRoute(hash || routeHashFromPathname(window.location.pathname));

      // Handle route
      switch (route.view) {
        case 'issue':
          // Issue detail view
          this.view = 'issues'; // Keep issues as backdrop
          if (route.params.id) {
            // Reset state when switching issues
            this.showDepGraph = false;
            this.whatIfResult = null;
            this.selectedIssue = getIssue(route.params.id);
            // Update nav list from current issues
            if (this.issues.length) {
              this.issueNavList = this.issues.map(i => i.id);
            }
          }
          break;

        case 'issues':
          this.view = 'issues';
          this.selectedIssue = null;
          this.showDepGraph = false;
          this.whatIfResult = null;
          this.filters = { ...this.filters, ...urlState.filters };
          this.sort = urlState.sort;
          this.searchQuery = urlState.searchQuery;
          this.page = 1;
          this.loadIssues();
          break;

        case 'insights':
          this.view = 'insights';
          this.selectedIssue = null;
          break;

        case 'graph':
          this.view = 'graph';
          this.selectedIssue = null;
          this.$nextTick(() => {
            this.initForceGraphView();
          });
          break;

        default:
          this.view = 'dashboard';
          this.selectedIssue = null;
      }
    },

    /**
     * Initialize (or refresh) the interactive force-graph view.
     * This is invoked when navigating to #/graph.
     */
    async initForceGraphView() {
      if (this.forceGraphLoading) return;

      this.forceGraphLoading = true;
      this.forceGraphError = null;

      try {
        // Check that required dependencies are available
        if (typeof window.ForceGraph !== 'function' || typeof window.d3 === 'undefined') {
          throw new Error('force-graph dependencies not loaded');
        }

        // Check database is ready
        if (!DB_STATE.db) {
          throw new Error('Database not loaded yet');
        }

        // Wait for container to be visible (Alpine x-show transition)
        const container = document.getElementById('graph-container');
        if (!container) {
          throw new Error('Graph container not found');
        }

        // Small delay to ensure container is visible after x-show transition
        await new Promise(resolve => setTimeout(resolve, 50));

        // Stage 1: Loading data from database
        this.graphLoadingStage = 'loading-data';

        // Check for empty data BEFORE initializing ForceGraph to avoid wasteful init
        const { issues, dependencies } = getGraphViewData();

        if (!issues || issues.length === 0) {
          console.warn('[ForceGraph] No issues to display');
          container.innerHTML = `
            <div class="flex flex-col items-center justify-center h-full text-gray-500 dark:text-gray-400">
              <svg class="w-16 h-16 mb-4 opacity-50" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                <path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5"
                      d="M9 19v-6a2 2 0 00-2-2H5a2 2 0 00-2 2v6a2 2 0 002 2h2a2 2 0 002-2zm0 0V9a2 2 0 012-2h2a2 2 0 012 2v10m-6 0a2 2 0 002 2h2a2 2 0 002-2m0 0V5a2 2 0 012-2h2a2 2 0 012 2v14a2 2 0 01-2 2h-2a2 2 0 01-2-2z"/>
              </svg>
              <p class="text-lg font-medium">No Issues to Display</p>
              <p class="text-sm mt-1">The project has no issues in the database.</p>
            </div>`;
          this.forceGraphLoading = false;
          this.graphLoadingStage = null;
          return;
        }

        // Stage 2: Initializing graph engine and computing metrics
        this.graphLoadingStage = 'computing-metrics';

        // Best-effort: ensure the graph WASM module is available for graph.js.
        if (typeof window.bvGraphWasm === 'undefined') {
          this.graphReady = await initGraphEngine();
          DIAGNOSTICS.graphWasm = this.graphReady;
        }

        if (!this.forceGraphModule) {
          this.forceGraphModule = await import('./graph.js');
        }

        // Always use dynamic force simulation - it produces much better layouts
        // Pre-computed positions are still exported but only used for metrics, not positions
        let precomputedLayout = null;
        console.log('[ForceGraph] Using live force simulation for optimal layout');

        // Stage 3: Initializing graph visualization
        this.graphLoadingStage = 'init';

        if (!this.forceGraphReady) {
          await this.forceGraphModule.initGraph('graph-container');
          this.forceGraphReady = true;

          // Register event listeners once (inside forceGraphReady check to avoid duplicates)
          // Events are dispatched on document, so listen there
          document.addEventListener('bv-graph:nodeClick', (e) => {
            const node = e.detail?.node;
            if (node) {
              this.graphDetailNode = node;
              console.log('[Viewer] Node selected for detail:', node.id);
              // Resize graph after detail pane opens (wait for transition)
              setTimeout(() => this.resizeForceGraph(), 350);
            }
          });
          document.addEventListener('bv-graph:backgroundClick', () => {
            this.graphDetailNode = null;
            // Resize graph after detail pane closes
            setTimeout(() => this.resizeForceGraph(), 250);
          });

          // Sync heatmap state when toggled via keyboard shortcut
          document.addEventListener('bv-graph:heatmapToggle', (e) => {
            this.graphHeatmapActive = e.detail?.active ?? false;
          });

          // Sync metric state when changed
          document.addEventListener('bv-graph:metricChange', (e) => {
            this.graphSizeMetric = e.detail?.metric ?? 'pagerank';
          });

          // Track simulation progress for loading indicator
          document.addEventListener('bv-graph:simulationProgress', (e) => {
            this.graphSimulationProgress = e.detail?.progress ?? 0;
            this.graphSimulationDone = e.detail?.done ?? false;
            if (e.detail?.done) {
              // Clear progress and stage after a short delay
              setTimeout(() => {
                this.graphSimulationProgress = null;
                this.graphLoadingStage = null;
              }, 500);
            }
          });
        }

        // Stage 4: Running force simulation
        this.graphLoadingStage = 'simulating';
        this.graphSimulationProgress = 0;
        this.graphSimulationDone = false;

        console.log(`[ForceGraph] Loading ${issues.length} issues, ${dependencies.length} dependencies`);
        this.forceGraphModule.loadData(issues, dependencies, precomputedLayout);

        // Try to load history data for time-travel feature (bv-z38b)
        // Use cache-busting to avoid stale data from CDN
        try {
          const historyResp = await fetch(`./data/history.json?_t=${Date.now()}`);
          if (historyResp.ok) {
            const historyData = await historyResp.json();
            if (this.forceGraphModule.initTimeTravel) {
              this.forceGraphModule.initTimeTravel(historyData);
              console.log('[Viewer] Time-travel history loaded');
            }
          }
        } catch (histErr) {
          // history.json is optional, silently ignore if not found
          console.log('[Viewer] No history.json found (optional for time-travel)');
        }

        // Match canvas size to container for crisp rendering.
        // (reuse container from earlier in this scope)
        const graph = this.forceGraphModule.getGraph?.();
        if (container && graph && typeof graph.width === 'function' && typeof graph.height === 'function') {
          graph.width(container.clientWidth);
          graph.height(container.clientHeight);
        }
      } catch (err) {
        console.error('[ForceGraph] init failed:', err);
        this.forceGraphError = err?.message || String(err);
        this.forceGraphReady = false;
        showToast(`Graph view failed: ${this.forceGraphError}`, 'error');

        const container = document.getElementById('graph-container');
        if (container) {
          container.innerHTML = '<p class="text-gray-500 dark:text-gray-400 text-center py-8">Graph failed to load.</p>';
        }
      } finally {
        this.forceGraphLoading = false;
      }
    },

    /**
     * Resize the ForceGraph canvas to fit its container
     * Called when detail pane opens/closes to adjust graph area
     */
    resizeForceGraph() {
      if (!this.forceGraphModule || !this.forceGraphReady) return;

      const container = document.getElementById('graph-container');
      const graph = this.forceGraphModule.getGraph?.();

      if (container && graph && typeof graph.width === 'function') {
        graph.width(container.clientWidth);
        graph.height(container.clientHeight);
      }
    },

    /**
     * Zoom in on the graph by 50%
     */
    graphZoomIn() {
      if (!this.forceGraphModule || !this.forceGraphReady) return;
      const graph = this.forceGraphModule.getGraph?.();
      if (graph && typeof graph.zoom === 'function') {
        const currentZoom = graph.zoom();
        graph.zoom(currentZoom * 1.5, 300);
      }
    },

    /**
     * Zoom out on the graph by 50%
     */
    graphZoomOut() {
      if (!this.forceGraphModule || !this.forceGraphReady) return;
      const graph = this.forceGraphModule.getGraph?.();
      if (graph && typeof graph.zoom === 'function') {
        const currentZoom = graph.zoom();
        graph.zoom(currentZoom / 1.5, 300);
      }
    },

    /**
     * Fit the graph to view all nodes
     */
    graphZoomToFit() {
      if (!this.forceGraphModule || !this.forceGraphReady) return;
      const graph = this.forceGraphModule.getGraph?.();
      if (graph && typeof graph.zoomToFit === 'function') {
        graph.zoomToFit(400, 50);
      }
    },

    /**
     * Search for a node in the graph and center on it
     */
    graphSearchNode(query) {
      if (!this.forceGraphModule || !this.forceGraphReady || !query) return null;
      const graph = this.forceGraphModule.getGraph?.();
      if (!graph) return null;

      // Get graph data safely
      const graphData = graph.graphData?.();
      if (!graphData || !graphData.nodes) return null;

      // Search by ID or title
      const q = query.toLowerCase().trim();
      if (!q) return null;

      const found = graphData.nodes.find(n =>
        (n.id && n.id.toLowerCase().includes(q)) ||
        (n.title && n.title.toLowerCase().includes(q))
      );

      if (found && typeof found.x === 'number' && typeof found.y === 'number') {
        // Center view on the node
        graph.centerAt(found.x, found.y, 500);
        graph.zoom(2, 500);
        // Select it via the module's selection function if available
        if (this.forceGraphModule.selectNode) {
          this.forceGraphModule.selectNode(found.id);
        }
        return found;
      }
      return null;
    },

    /**
     * Load issues based on current filters
     */
    loadIssues() {
      const offset = (this.page - 1) * this.pageSize;
      const filters = {
        ...this.filters,
        search: this.searchQuery,
      };

      if (this.searchQuery) {
        this.issues = searchIssues(this.searchQuery, {
          mode: this.searchMode,
          preset: this.searchPreset,
          limit: this.pageSize,
          offset,
          filters,
        });
        this.totalIssues = countSearchIssues(this.searchQuery, filters);
      } else {
        this.issues = queryIssues(filters, this.sort, this.pageSize, offset);
        this.totalIssues = countIssues(filters);
      }

      // Sync URL state (only on issues view)
      if (this.view === 'issues') {
        syncFiltersToURL('issues', this.filters, this.sort, this.searchQuery);
      }
    },

    /**
     * Apply filter and reload (resets to page 1)
     */
    applyFilter() {
      this.page = 1;
      this.loadIssues();
    },

    /**
     * Alias for applyFilter (used by dashboard click handlers)
     */
    applyFilters() {
      this.applyFilter();
    },

    /**
     * Clear all filters
     */
    clearFilters() {
      this.filters = {
        status: [],
        type: [],
        priority: [],
        labels: [],
        assignee: '',
        hasBlockers: null,
        isBlocking: null,
      };
      this.searchQuery = '';
      this.sort = 'priority';
      this.page = 1;
      this.loadIssues();
    },

    /**
     * Check if any filters are active
     */
    get hasActiveFilters() {
      return this.filters.status?.length > 0 ||
             this.filters.type?.length > 0 ||
             this.filters.priority?.length > 0 ||
             this.filters.labels?.length > 0 ||
             this.filters.assignee ||
             this.filters.hasBlockers !== null ||
             this.filters.isBlocking !== null ||
             this.searchQuery;
    },

    /**
     * Toggle a value in a multi-select filter array
     */
    toggleFilter(filterName, value) {
      if (!Array.isArray(this.filters[filterName])) {
        this.filters[filterName] = [];
      }
      const index = this.filters[filterName].indexOf(value);
      if (index === -1) {
        this.filters[filterName].push(value);
      } else {
        this.filters[filterName].splice(index, 1);
      }
      this.applyFilter();
    },

    /**
     * Check if a value is selected in a multi-select filter
     */
    isFilterSelected(filterName, value) {
      if (!Array.isArray(this.filters[filterName])) return false;
      return this.filters[filterName].includes(value);
    },

    /**
     * Search issues
     */
    search() {
      this.page = 1;
      this.loadIssues();
    },

    /**
     * Pagination
     */
    nextPage() {
      if (this.page * this.pageSize < this.totalIssues) {
        this.page++;
        this.loadIssues();
      }
    },

    prevPage() {
      if (this.page > 1) {
        this.page--;
        this.loadIssues();
      }
    },

    /**
     * Select an issue and open the modal without changing routes.
     * Used by keyboard shortcuts and the force-graph view.
     */
    selectIssue(id) {
      if (!id) return;
      const issue = getIssue(id);
      if (!issue) {
        showToast(`Issue not found: ${id}`, 'warning');
        return;
      }

      this.showDepGraph = false;
      this.whatIfResult = null;
      this.selectedIssue = issue;

      if (this.issues.length) {
        this.issueNavList = this.issues.map(i => i.id);
      }
    },

    /**
     * Show issue detail (navigates to issue route)
     */
    showIssue(id) {
      navigateToIssue(id);
    },

    /**
     * Close issue detail (navigates back)
     */
    closeIssue() {
      // Reset issue detail state
      this.selectedIssue = null;
      this.showDepGraph = false;
      this.whatIfResult = null;

      // Navigate back
      const currentView = this.view;
      if (currentView === 'issues') {
        navigateToIssues(this.filters, this.sort, this.searchQuery);
      } else {
        navigate('/' + currentView);
      }
    },

    /**
     * Navigate to next/previous issue in the list
     * @param {number} direction - 1 for next, -1 for previous
     */
    navigateIssue(direction) {
      if (!this.selectedIssue) return;

      // Build navigation list from current issues if not set
      if (!this.issueNavList.length && this.issues.length) {
        this.issueNavList = this.issues.map(i => i.id);
      }

      // Find current position
      const currentId = this.selectedIssue.id;
      const currentIndex = this.issueNavList.indexOf(currentId);

      if (currentIndex === -1) {
        // Current issue not in nav list, just stay
        return;
      }

      // Calculate new index with wrapping
      const newIndex = (currentIndex + direction + this.issueNavList.length) % this.issueNavList.length;
      const newId = this.issueNavList[newIndex];

      // Reset state and navigate/select
      this.showDepGraph = false;
      this.whatIfResult = null;
      const route = parseRoute(window.location.hash);
      if (route.view === 'issue') {
        navigateToIssue(newId);
      } else {
        this.selectIssue(newId);
      }
    },

    /**
     * Parse JSON labels string to array
     */
    parseLabels(labelsStr) {
      if (!labelsStr) return [];
      try {
        const labels = JSON.parse(labelsStr);
        return Array.isArray(labels) ? labels : [];
      } catch {
        return [];
      }
    },

    /**
     * Render Mermaid dependency graph for the selected issue
     */
    async renderDepGraph() {
      if (!this.selectedIssue || !this.$refs.depGraph) return;

      const issue = this.selectedIssue;
      const blockedBy = (issue.blocked_by_ids || '').split(',').filter(Boolean).map(s => s.trim());
      const blocks = (issue.blocks_ids || '').split(',').filter(Boolean).map(s => s.trim());

      if (blockedBy.length === 0 && blocks.length === 0) {
        this.$refs.depGraph.innerHTML = '<p class="text-gray-500 dark:text-gray-400 text-center">No dependencies</p>';
        return;
      }

      // Build Mermaid flowchart
      let diagram = 'flowchart TB\n';

      // Sanitize ID for mermaid (replace special chars)
      const sanitizeId = (id) => id.replace(/[^a-zA-Z0-9]/g, '_');
      const currentId = sanitizeId(issue.id);

      // Style for current node
      diagram += `  ${currentId}["${issue.id}"]\n`;
      diagram += `  style ${currentId} fill:#0ea5e9,stroke:#0284c7,color:#fff\n`;

      // Add blocked-by nodes (upstream)
      for (const depId of blockedBy) {
        const nodeId = sanitizeId(depId);
        diagram += `  ${nodeId}["${depId}"]\n`;
        diagram += `  ${nodeId} --> ${currentId}\n`;
        diagram += `  style ${nodeId} fill:#fef3c7,stroke:#f59e0b\n`;
      }

      // Add blocks nodes (downstream)
      for (const depId of blocks) {
        const nodeId = sanitizeId(depId);
        diagram += `  ${nodeId}["${depId}"]\n`;
        diagram += `  ${currentId} --> ${nodeId}\n`;
        diagram += `  style ${nodeId} fill:#fee2e2,stroke:#ef4444\n`;
      }

      // Add click handlers
      const allIds = [issue.id, ...blockedBy, ...blocks];
      for (const id of allIds) {
        const nodeId = sanitizeId(id);
        diagram += `  click ${nodeId} call window.beadsViewer.navigateToIssue("${id}")\n`;
      }

      try {
        // Render the diagram
        const { svg } = await mermaid.render('dep-graph-' + Date.now(), diagram);
        this.$refs.depGraph.innerHTML = svg;
      } catch (err) {
        console.warn('Mermaid render failed:', err);
        this.$refs.depGraph.innerHTML = '<p class="text-red-500 text-center text-sm">Failed to render graph</p>';
      }
    },

    /**
     * Toggle dark mode
     */
    toggleDarkMode() {
      this.darkMode = !this.darkMode;
      localStorage.setItem('darkMode', this.darkMode);
      document.documentElement.classList.toggle('dark', this.darkMode);

      // Re-initialize Mermaid with new theme
      if (window.reinitializeMermaid) {
        window.reinitializeMermaid();
      }

      // Re-render visible Mermaid graph if any
      if (this.showDepGraph && this.selectedIssue) {
        this.renderDepGraph();
      }
    },

    // ========================================================================
    // Graph Engine Methods
    // ========================================================================

    /**
     * Recalculate graph metrics for currently filtered issues
     */
    recalculateForFilter() {
      if (!this.graphReady) return;
      const ids = this.issues.map(i => i.id);
      this.graphMetrics = recalculateMetrics(ids);
    },

    /**
     * Compute what-if cascade impact for an issue
     */
    computeWhatIf(issueId) {
      if (!this.graphReady) return;
      this.whatIfResult = whatIfClose(issueId);
    },

    /**
     * Clear what-if result
     */
    clearWhatIf() {
      this.whatIfResult = null;
    },

    /**
     * Refresh top-k set
     */
    refreshTopKSet() {
      if (!this.graphReady) return;
      this.topKSet = getTopKSet(5);
    },

    /**
     * Get top issues by cascade impact
     */
    getTopImpact(limit = 10) {
      if (!this.graphReady) return [];
      return topWhatIf(limit);
    },

    /**
     * Get cycle break suggestions
     */
    getCycleBreaks(limit = 5) {
      if (!this.graphReady) return null;
      return getCycleBreakSuggestions(limit);
    },

    /**
     * Toggle critical path highlighting
     */
    toggleCriticalPath() {
      if (!this.graphReady) {
        showToast('Graph engine not ready', 'warning');
        return;
      }

      this.showCriticalPath = !this.showCriticalPath;

      if (this.showCriticalPath) {
        this.criticalPathData = getCriticalPathSequence();
        if (!this.criticalPathData || this.criticalPathData.path.length === 0) {
          showToast('No critical path found (may have cycles)', 'info');
          this.showCriticalPath = false;
          return;
        }
        showToast(`Critical path: ${this.criticalPathData.length} issues deep`, 'success');
        // Start animation
        this.animateCriticalPath();
      } else {
        this.criticalPathData = null;
        this.criticalPathAnimationStep = -1;
      }
    },

    /**
     * Animate critical path traversal
     */
    async animateCriticalPath() {
      if (!this.criticalPathData || !this.showCriticalPath) return;

      const path = this.criticalPathData.path;
      this.criticalPathAnimationStep = -1;

      // Animate through each node with staggered timing
      for (let i = 0; i < path.length; i++) {
        if (!this.showCriticalPath) break; // Stop if toggled off

        this.criticalPathAnimationStep = i;
        await new Promise(resolve => setTimeout(resolve, 300));
      }

      // Keep final state highlighted
      this.criticalPathAnimationStep = path.length - 1;
    },

    /**
     * Apply a layout preset to the graph (bv-97)
     * @param {string} presetName - One of: 'force', 'compact', 'spread', 'orthogonal', 'radial', 'cluster'
     */
    applyGraphPreset(presetName) {
      if (!this.forceGraphModule) {
        showToast('Graph not initialized', 'warning');
        return;
      }
      if (this.forceGraphModule.applyPreset) {
        const success = this.forceGraphModule.applyPreset(presetName);
        if (success) {
          const presets = this.forceGraphModule.getLayoutPresets?.() || {};
          const preset = presets[presetName];
          showToast(`Layout: ${preset?.name || presetName}`, 'info');
        }
      } else {
        showToast('Presets not available', 'warning');
      }
    },

    /**
     * Get available layout presets (bv-97)
     */
    getGraphPresets() {
      if (!this.forceGraphModule?.getLayoutPresets) return {};
      return this.forceGraphModule.getLayoutPresets();
    },

    /**
     * Toggle heatmap mode in the graph
     */
    toggleGraphHeatmap() {
      if (!this.forceGraphModule?.toggleHeatmap) {
        showToast('Heatmap not available', 'warning');
        return;
      }
      this.graphHeatmapActive = this.forceGraphModule.toggleHeatmap();
      showToast(this.graphHeatmapActive ? 'Heatmap ON' : 'Heatmap OFF', 'info');
    },

    /**
     * Set the metric used for heatmap coloring and node sizing
     */
    setGraphSizeMetric(metric) {
      if (!this.forceGraphModule?.setSizeMetric) {
        showToast('Metric selection not available', 'warning');
        return;
      }
      this.forceGraphModule.setSizeMetric(metric);
      this.graphSizeMetric = metric;
      const metricLabels = {
        pagerank: 'PageRank',
        betweenness: 'Betweenness Centrality',
        critical: 'Critical Path Depth',
        indegree: 'In-Degree (Blockers)'
      };
      showToast(`Metric: ${metricLabels[metric] || metric}`, 'info');
    },

    /**
     * Check if an issue is on the critical path
     */
    isOnCriticalPath(issueId) {
      if (!this.showCriticalPath || !this.criticalPathData) return false;
      return this.criticalPathData.path.includes(issueId);
    },

    /**
     * Check if an issue is the current animation step
     */
    isCriticalPathAnimating(issueId) {
      if (!this.showCriticalPath || !this.criticalPathData) return false;
      const idx = this.criticalPathData.path.indexOf(issueId);
      return idx !== -1 && idx === this.criticalPathAnimationStep;
    },

    /**
     * Get critical path position for an issue (1-indexed)
     */
    getCriticalPathPosition(issueId) {
      if (!this.criticalPathData) return null;
      const idx = this.criticalPathData.path.indexOf(issueId);
      return idx !== -1 ? idx + 1 : null;
    },

    /**
     * Format date helper (relative for recent, absolute for older)
     */
    formatDate,

    /**
     * Format date helper (always absolute with time)
     */
    formatDateFull,

    /**
     * Safe number formatter (returns em-dash for NaN/undefined/null/Infinity)
     */
    safeNum,

    /**
     * Render markdown helper
     */
    renderMarkdown,

    /**
     * Render markdown as inline HTML (for excerpts with line-clamp)
     */
    renderMarkdownInline,

    /**
     * Strip markdown to plain text (for excerpts)
     */
    stripMarkdownToText,

    /**
     * Get issue by ID (wrapper for templates)
     */
    getIssue,

    /**
     * Dismiss the current error modal
     */
    dismissError() {
      if (this.globalError && this.globalError.dismissible) {
        this.globalError = null;
        clearError();
      }
    },

    /**
     * Remove a toast notification
     */
    removeToast(id) {
      this.toasts = this.toasts.filter(t => t.id !== id);
    },
  };
}

// Export for use in graph integration
window.beadsViewer = {
  // Database
  DB_STATE,
  loadDatabase,
  execQuery,
  queryIssues,
  countIssues,
  getIssue,
  getIssueDependencies,
  getStats,
  getMeta,
  getFilterOptions,
  getUniqueLabels,
  searchIssues,

  // URL State & Router
  filtersToURL,
  filtersFromURL,
  syncFiltersToURL,
  parseRoute,
  routeHashFromPathname,
  matchPattern,
  navigate,
  navigateToIssue,
  navigateToIssues,
  navigateToDashboard,
  goBack,

  // Graph Engine
  GRAPH_STATE,
  initGraphEngine,
  buildClosedSet,
  recalculateMetrics,
  whatIfClose,
  topWhatIf,
  getActionableIssues,
  getCycleBreakSuggestions,
  getCriticalPathSequence,
  getTopKSet,

  // WASM Fallback
  WASM_STATUS,
  checkWASMSupport,
  enableFallbackMode,

  // WASM Memory Management
  WASM_ALLOCATIONS,
  withSubgraph,
  cleanupWasm,
  getWasmMemoryStats,

  // Insights helpers
  getTopByBetweenness,
  getTopByCriticalPath,
  getCycleInfo,

  // Error handling
  ERROR_STATE,
  DIAGNOSTICS,
  showError,
  clearError,
  safeQuery,
  showToast,
};
