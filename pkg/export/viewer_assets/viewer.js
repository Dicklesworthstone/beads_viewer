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

const VIEWER_SCRIPT_SRC = (typeof document !== 'undefined' && document.currentScript?.src) || '';

/**
 * Extract the ?v=... cache-busting query suffix stamped onto viewer.js by
 * CopyEmbeddedAssets so dynamic ES module imports fetch matching deployment revisions.
 */
function getViewerAssetQuerySuffix() {
  try {
    const raw = VIEWER_SCRIPT_SRC ||
      (typeof document !== 'undefined'
        ? document.querySelector('script[src*="viewer.js"]')?.getAttribute('src') || ''
        : '');
    if (!raw) return '';
    const parsed = new URL(raw, typeof window !== 'undefined' ? window.location.href : 'http://localhost/');
    const v = parsed.searchParams.get('v');
    return v ? `?v=${encodeURIComponent(v)}` : '';
  } catch {
    return '';
  }
}

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

let _toastSequence = 0;
function nextToastId() {
  _toastSequence += 1;
  return `${Date.now()}-${_toastSequence}`;
}

/**
 * Show a toast notification (non-blocking)
 * @param {string} message - Toast message
 * @param {string} type - Toast type: 'info' | 'success' | 'warning' | 'error'
 */
function showToast(message, type = 'info') {
  // This will be picked up by the Alpine toast component
  window.dispatchEvent(new CustomEvent('show-toast', {
    detail: { message, type, id: nextToastId() }
  }));
}

// Database state
const DB_STATE = {
  sql: null,            // sql.js library instance
  db: null,             // Database instance
  cacheKey: null,       // OPFS cache key (hash)
  source: 'unknown',    // 'network' | 'cache' | 'chunks'
  fts5: null,           // Actual browser SQLite capability, not exported schema.
  jsonEach: null,       // Cached SQLite json_each() table-valued function capability
  jsonEachDb: null,     // DB instance associated with jsonEach capability
  referenceNowMs: null, // Snapshot reference timestamp (ms) for deterministic recency scoring
  referenceDb: null,    // DB instance associated with referenceNowMs
};
if (typeof window !== 'undefined') {
  window.DB_STATE = DB_STATE;
}

function supportsJsonEach() {
  if (!DB_STATE.db) return false;
  if (DB_STATE.jsonEachDb === DB_STATE.db && typeof DB_STATE.jsonEach === 'boolean') {
    return DB_STATE.jsonEach;
  }
  DB_STATE.jsonEachDb = DB_STATE.db;
  try {
    const stmt = DB_STATE.db.prepare(`SELECT value FROM json_each('["probe"]')`);
    let ok = false;
    if (stmt.step()) {
      const row = stmt.get();
      ok = Array.isArray(row) && row[0] === 'probe';
    }
    stmt.free();
    DB_STATE.jsonEach = ok;
  } catch {
    DB_STATE.jsonEach = false;
  }
  return DB_STATE.jsonEach;
}

function formatEstimatedMinutes(minutes) {
  const num = Number(minutes);
  if (!Number.isFinite(num) || num <= 0) return '';
  if (num < 60) return `${Math.round(num)}m`;
  const hours = Math.floor(num / 60);
  const rem = Math.round(num % 60);
  return rem > 0 ? `${hours}h ${rem}m` : `${hours}h`;
}

const KNOWN_STATUS_BADGES = new Set([
  'open',
  'in_progress',
  'blocked',
  'closed',
  'deferred',
  'draft',
  'review',
  'pinned',
  'hooked',
]);

function statusBadgeClass(status) {
  const normalized = String(status || '').trim().toLowerCase();
  return KNOWN_STATUS_BADGES.has(normalized) ? `status-${normalized}` : 'status-deferred';
}

function getReferenceNowMs() {
  if (!DB_STATE.db) {
    return (typeof window !== 'undefined' && Number.isFinite(window.BV_REFERENCE_NOW_MS) && window.BV_REFERENCE_NOW_MS > 0)
      ? window.BV_REFERENCE_NOW_MS
      : Date.now();
  }
  if (DB_STATE.referenceDb === DB_STATE.db && Number.isFinite(DB_STATE.referenceNowMs) && DB_STATE.referenceNowMs > 0) {
    return DB_STATE.referenceNowMs;
  }

  let resolved = null;
  try {
    const metaRows = execQuery(
      `SELECT key, value FROM export_meta WHERE key IN ('generated_at', 'exported_at', 'created_at', 'timestamp')`
    );
    for (const key of ['generated_at', 'exported_at', 'created_at', 'timestamp']) {
      const hit = metaRows.find(r => r.key === key && r.value);
      if (hit) {
        const parsed = Date.parse(hit.value);
        if (Number.isFinite(parsed) && parsed > 0) {
          resolved = parsed;
          break;
        }
      }
    }
  } catch {
    // Ignore missing export_meta table
  }

  if (!resolved) {
    try {
      const maxUpdated = execScalar(
        `SELECT MAX(updated_at) FROM issues WHERE updated_at IS NOT NULL AND updated_at != ''`
      );
      if (maxUpdated) {
        const parsed = Date.parse(maxUpdated);
        if (Number.isFinite(parsed) && parsed > 0) {
          resolved = parsed;
        }
      }
    } catch {
      // Ignore fallback query errors
    }
  }

  const finalNow = resolved || Date.now();
  DB_STATE.referenceDb = DB_STATE.db;
  DB_STATE.referenceNowMs = finalNow;
  if (typeof window !== 'undefined') {
    window.BV_REFERENCE_NOW_MS = finalNow;
  }
  return finalNow;
}

// Graph engine state (WASM)
const GRAPH_STATE = {
  wasm: null,          // WASM module (bv_graph.js)
  graph: null,         // Full DiGraph instance (PageRank, HITS, Eigenvector)
  nodeMap: null,       // Map<string, number> - issue ID to full graph node index
  activeGraph: null,   // Active-only DiGraph instance (excludes closed/tombstone for cycles, slack, critical path, betweenness, k-core, bridges, articulation)
  activeNodeMap: null, // Map<string, number> - active issue ID to activeGraph node index
  whatIfGraph: null,   // Companion DiGraph for actionable topWhatIf/topK/parallelCut (includes inherited open parent-child gates & readiness withholding)
  whatIfModel: null,   // Pre-indexed parent-child + blocking readiness model for arbitrary-target whatIfClose / whatIfCloseBatch / allWhatIf
  ready: false,        // true when graph is loaded
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
function withSubgraph(indices, fn, baseGraph = GRAPH_STATE.graph) {
  if (!GRAPH_STATE.ready || !baseGraph) {
    console.warn('[WASM Memory] Cannot create subgraph: graph not ready');
    return null;
  }

  const indicesArray = indices instanceof Uint32Array
    ? indices
    : new Uint32Array(indices);

  const subgraph = baseGraph.subgraph(indicesArray);
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

  // Free the companion what-if graph
  if (GRAPH_STATE.whatIfGraph && typeof GRAPH_STATE.whatIfGraph.free === 'function') {
    try {
      GRAPH_STATE.whatIfGraph.free();
    } catch (e) {
      console.warn('[WASM Memory] Error freeing what-if graph:', e);
    }
    GRAPH_STATE.whatIfGraph = null;
  }
  GRAPH_STATE.whatIfModel = null;

  // Free the active graph
  if (GRAPH_STATE.activeGraph && typeof GRAPH_STATE.activeGraph.free === 'function') {
    try {
      GRAPH_STATE.activeGraph.free();
    } catch (e) {
      console.warn('[WASM Memory] Error freeing active graph:', e);
    }
    GRAPH_STATE.activeGraph = null;
    GRAPH_STATE.activeNodeMap = null;
  }

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
      // Load sql.js strictly from local vendored assets (enforced by 'self' CSP).
      const script = document.createElement('script');
      script.src = './vendor/sql-wasm.js';
      document.head.appendChild(script);
      await new Promise((res, rej) => {
        script.onload = res;
        script.onerror = () => rej(new Error('Failed to load local ./vendor/sql-wasm.js'));
      });
      const sqlJs = window.initSqlJs;

      const SQL = await sqlJs({
        locateFile: file => `./vendor/${file}`
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

function ensureSqlFunctions(db) {
  if (!db || typeof db.create_function !== 'function') {
    return false;
  }
  if (db._bvFunctionsRegistered) {
    return true;
  }
  try {
    db.create_function('bv_has_label', (labelsJson, target) => {
      try {
        const parsed = JSON.parse(labelsJson || '[]');
        return Array.isArray(parsed) && parsed.includes(String(target)) ? 1 : 0;
      } catch {
        return 0;
      }
    });
    if (!db._bvExportWrapped && typeof db.export === 'function') {
      const rawExport = db.export.bind(db);
      db.export = function (...args) {
        const exported = rawExport(...args);
        db._bvFunctionsRegistered = false;
        ensureSqlFunctions(db);
        return exported;
      };
      db._bvExportWrapped = true;
    }
    db._bvFunctionsRegistered = true;
    return true;
  } catch (err) {
    console.warn('[SQL] Failed to register bv_has_label UDF:', err);
    return false;
  }
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
      ensureSqlFunctions(DB_STATE.db);
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
    ensureSqlFunctions(DB_STATE.db);
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

// Match model.DependencyType.IsBlocking, including legacy empty types.
const BLOCKING_DEPENDENCY_TYPES_SQL = "('', 'blocks', 'conditional-blocks', 'waits-for')";

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
    // Dynamic import of the WASM module (propagate deployment cache-buster if present)
    const suffix = getViewerAssetQuerySuffix();
    const wasmModule = await import('./vendor/bv_graph.js' + suffix);
    await wasmModule.default({
      module_or_path: new URL('./vendor/bv_graph_bg.wasm' + suffix, window.location.href),
    }); // Initialize WASM

    // Expose for other modules (e.g., graph.js force-graph view) to reuse.
    window.bvGraphWasm = wasmModule;

    if (GRAPH_STATE.whatIfGraph && typeof GRAPH_STATE.whatIfGraph.free === 'function') {
      try { GRAPH_STATE.whatIfGraph.free(); } catch {}
      GRAPH_STATE.whatIfGraph = null;
    }
    GRAPH_STATE.whatIfModel = null;
    if (GRAPH_STATE.activeGraph && typeof GRAPH_STATE.activeGraph.free === 'function') {
      try { GRAPH_STATE.activeGraph.free(); } catch {}
      GRAPH_STATE.activeGraph = null;
    }
    if (GRAPH_STATE.graph && typeof GRAPH_STATE.graph.free === 'function') {
      try { GRAPH_STATE.graph.free(); } catch {}
      GRAPH_STATE.graph = null;
    }

    GRAPH_STATE.wasm = wasmModule;
    GRAPH_STATE.graph = new wasmModule.DiGraph();
    GRAPH_STATE.nodeMap = new Map();
    GRAPH_STATE.activeGraph = new wasmModule.DiGraph();
    GRAPH_STATE.activeNodeMap = new Map();

    // Load graph data from SQLite
    if (!DB_STATE.db) {
      console.warn('[Graph] Database not loaded yet');
      return false;
    }

    const resolvedIds = getResolvedIssueIDs();

    // Isolated issues participate in metrics too. Edges alone omit them.
    for (const row of execQuery('SELECT id FROM issues ORDER BY id')) {
      GRAPH_STATE.nodeMap.set(row.id, GRAPH_STATE.graph.addNode(row.id));
      if (!resolvedIds.has(row.id)) {
        GRAPH_STATE.activeNodeMap.set(row.id, GRAPH_STATE.activeGraph.addNode(row.id));
      }
    }

    const deps = execQuery(`
      SELECT issue_id, depends_on_id
      FROM dependencies
      WHERE COALESCE(type, 'blocks') IN ${BLOCKING_DEPENDENCY_TYPES_SQL}
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

      // Active-only graph excludes closed/tombstone endpoints so historical closed
      // cycles cannot poison live cycle detection, critical paths, or slack.
      if (!resolvedIds.has(from) && !resolvedIds.has(to)) {
        if (!GRAPH_STATE.activeNodeMap.has(from)) {
          GRAPH_STATE.activeNodeMap.set(from, GRAPH_STATE.activeGraph.addNode(from));
        }
        if (!GRAPH_STATE.activeNodeMap.has(to)) {
          GRAPH_STATE.activeNodeMap.set(to, GRAPH_STATE.activeGraph.addNode(to));
        }
        GRAPH_STATE.activeGraph.addEdge(
          GRAPH_STATE.activeNodeMap.get(from),
          GRAPH_STATE.activeNodeMap.get(to)
        );
      }
    }

    GRAPH_STATE.whatIfGraph = buildCompanionWhatIfGraph(wasmModule, resolvedIds, deps);

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
 * Preserve resolved prerequisite identities even when their rows are omitted.
 */
function getResolvedIssueIDs() {
  const ids = new Set(execQuery("SELECT id FROM issues WHERE LOWER(status) IN ('closed', 'tombstone')").map(row => row.id));
  const metadata = getMeta().resolved_issue_ids;
  if (metadata) {
    for (const id of JSON.parse(metadata)) ids.add(id);
  }
  return ids;
}

function isTruthyLifecycleFlag(val) {
  return val === true || val === 'true' || Number(val) > 0;
}

function isIssueLifecycleWithheld(issue, refNowMs) {
  if (!issue) return true;
  const st = String(issue.status || '').trim().toLowerCase();
  if (st !== 'open' && st !== 'in_progress') return true;
  if (
    isTruthyLifecycleFlag(issue.is_template) ||
    isTruthyLifecycleFlag(issue.pinned) ||
    isTruthyLifecycleFlag(issue.ephemeral)
  ) {
    return true;
  }
  if (issue.defer_until) {
    const deferMs = Date.parse(issue.defer_until);
    if (Number.isFinite(deferMs) && deferMs > refNowMs) return true;
  }
  return false;
}

function queryIssuesForWhatIf() {
  const cols = new Set(
    execQuery('PRAGMA table_info(issues)').map(r => String(r.name || '').toLowerCase())
  );
  const selectCols = ['id', 'status'];
  for (const optionalCol of ['defer_until', 'is_template', 'pinned', 'ephemeral']) {
    if (cols.has(optionalCol)) selectCols.push(optionalCol);
  }
  const map = new Map();
  for (const row of execQuery(`SELECT ${selectCols.join(', ')} FROM issues ORDER BY id`)) {
    map.set(row.id, row);
  }
  return map;
}

/**
 * Build companion DiGraph for what-if / top-k / parallel-cut with identical 0..N-1
 * indices plus inherited open parent-child prerequisite gates and an optional
 * unresolved sentinel at index N for missing/cyclic parents or lifecycle-withheld nodes.
 */
function buildCompanionWhatIfGraph(wasmModule, resolvedIds, blockingDeps) {
  const baseGraph = GRAPH_STATE.graph;
  const realCount = baseGraph.nodeCount();
  const wg = new wasmModule.DiGraph();
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
  for (const row of blockingDeps) {
    const fromIdx = GRAPH_STATE.nodeMap.get(row.issue_id);
    const toIdx = GRAPH_STATE.nodeMap.get(row.depends_on_id);
    if (fromIdx === undefined || toIdx === undefined) continue;
    wg.addEdge(fromIdx, toIdx);
    directBlockers[fromIdx].push(toIdx);
    directDependents[toIdx].push(fromIdx);
    let list = directBlockingById.get(row.issue_id);
    if (!list) {
      list = [];
      directBlockingById.set(row.issue_id, list);
    }
    list.push(toIdx);
  }

  const knownIssues = queryIssuesForWhatIf();
  const parentChildRows = execQuery(`
    SELECT issue_id, depends_on_id
    FROM dependencies
    WHERE type = 'parent-child'
    ORDER BY issue_id, depends_on_id
  `);

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

  for (const row of parentChildRows) {
    const childId = row.issue_id;
    const parentId = row.depends_on_id;
    if (!childId || resolvedIds.has(childId) || !knownIssues.has(childId)) continue;
    if (!parentId || resolvedIds.has(parentId)) {
      // Closed or tombstone parent stops ancestor gate inheritance.
      continue;
    }
    const childIdx = GRAPH_STATE.nodeMap.get(childId);
    if (!knownIssues.has(parentId)) {
      hasMissingParent.add(childId);
      if (childIdx !== undefined && childIdx < realCount) {
        hasDirectMissingParent[childIdx] = 1;
        hasParentChild = true;
      }
      continue;
    }
    hasParentChild = true;
    const parentIdx = GRAPH_STATE.nodeMap.get(parentId);
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

  const refNowMs = getReferenceNowMs();
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
    const withheld = isIssueLifecycleWithheld(issue, refNowMs);
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

  GRAPH_STATE.whatIfModel = {
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

function createWhatIfDepsEvaluator(model, closedMask) {
  const memo = new Uint8Array(model.realCount); // 0 = unknown, 1 = satisfied, 2 = unsatisfied
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
          if (closedMask[pIdx]) continue; // Closed or simulated-closed parent stops inheritance (readiness.go:604)
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

function evaluateWhatIfReadyMask(model, closedMask) {
  const ready = new Uint8Array(model.realCount);
  const depsOk = createWhatIfDepsEvaluator(model, closedMask);
  for (let i = 0; i < model.realCount; i++) {
    if (!closedMask[i] && model.lifecycleReady[i] && depsOk(i)) {
      ready[i] = 1;
    }
  }
  return ready;
}

function simulateWhatIfCloseIndices(model, targetIndices) {
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

  const beforeReady = evaluateWhatIfReadyMask(model, model.baseClosed);
  const simClosed = new Uint8Array(model.baseClosed);
  for (const idx of uniqueTargets) {
    simClosed[idx] = 1;
  }
  const afterReady = evaluateWhatIfReadyMask(model, simClosed);

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

    // Collect dependents woken by closing v: direct dependents of v, open children of v,
    // and open-parent descendant subtrees of both.
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
    const depsOk = createWhatIfDepsEvaluator(model, simClosed);
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

/** Return 1 for resolved nodes (closed or tombstone), 0 for unresolved nodes. */
function buildClosedSet() {
  if (!GRAPH_STATE.ready) return null;

  const targetGraph = GRAPH_STATE.whatIfGraph || GRAPH_STATE.graph;
  const n = targetGraph.nodeCount();
  const realCount = GRAPH_STATE.graph.nodeCount();
  const closed = new Uint8Array(n);

  for (const id of getResolvedIssueIDs()) {
    const idx = GRAPH_STATE.nodeMap.get(id);
    if (idx !== undefined && idx < realCount) {
      closed[idx] = 1;
    }
  }

  return closed;
}

function getOverviewColumnSet() {
  try {
    return new Set(
      execQuery('PRAGMA table_info(issue_overview_mv)').map(r => String(r.name || '').toLowerCase())
    );
  } catch {
    return new Set();
  }
}

function getOverviewWithheldGuardSQL() {
  const cols = getOverviewColumnSet();
  let guard = '';
  if (cols.has('is_template')) guard += ' AND COALESCE(is_template, 0) = 0';
  if (cols.has('pinned')) guard += ' AND COALESCE(pinned, 0) = 0';
  if (cols.has('ephemeral')) guard += ' AND COALESCE(ephemeral, 0) = 0';
  return guard;
}

/** Restrict selectable issues without marking excluded prerequisites closed. */
function buildCandidateSet(options = false) {
  const targetGraph = GRAPH_STATE.whatIfGraph || GRAPH_STATE.graph;
  const realCount = GRAPH_STATE.graph.nodeCount();
  const candidates = new Uint8Array(targetGraph.nodeCount());
  const actionableOnly = options === true || Boolean(options?.actionableOnly);
  const lifecycleOnly = Boolean(options && typeof options === 'object' && options.lifecycleOnly);
  const withheldGuard = getOverviewWithheldGuardSQL();

  if (lifecycleOnly) {
    const nowMs = getReferenceNowMs();
    const rows = execQuery(`
      SELECT id, defer_until
      FROM issue_overview_mv
      WHERE LOWER(status) IN ('open', 'in_progress')${withheldGuard}
      ORDER BY id
    `);
    for (const row of rows) {
      if (row.defer_until) {
        const deferMs = Date.parse(row.defer_until);
        if (Number.isFinite(deferMs) && deferMs > nowMs) continue;
      }
      const idx = GRAPH_STATE.nodeMap.get(row.id);
      if (idx !== undefined && idx < realCount) candidates[idx] = 1;
    }
    return candidates;
  }

  if (actionableOnly) {
    const nowMs = getReferenceNowMs();
    const rows = execQuery(`
      SELECT id, defer_until
      FROM issue_overview_mv
      WHERE is_actionable = 1
        AND LOWER(status) NOT IN ('closed', 'tombstone', 'deferred', 'draft', 'pinned')${withheldGuard}
      ORDER BY id
    `);
    for (const row of rows) {
      if (row.defer_until) {
        const deferMs = Date.parse(row.defer_until);
        if (Number.isFinite(deferMs) && deferMs > nowMs) continue;
      }
      const idx = GRAPH_STATE.nodeMap.get(row.id);
      if (idx !== undefined && idx < realCount) candidates[idx] = 1;
    }
    return candidates;
  }

  const rows = execQuery(
    `SELECT id FROM issue_overview_mv WHERE LOWER(status) != 'pinned'${withheldGuard} ORDER BY id`
  );
  for (const { id } of rows) {
    const idx = GRAPH_STATE.nodeMap.get(id);
    if (idx !== undefined && idx < realCount) candidates[idx] = 1;
  }
  return candidates;
}

/**
 * Recalculate graph metrics for a filtered set of issues
 */
function recalculateMetrics(issueIds) {
  if (!GRAPH_STATE.ready || !Array.isArray(issueIds) || issueIds.length === 0) return null;

  const start = performance.now();
  const indices = issueIds
    .map(id => GRAPH_STATE.nodeMap.get(id))
    .filter(idx => idx !== undefined);

  if (indices.length === 0) return null;

  // Extract subgraph for filtered issues - use withSubgraph for automatic cleanup
  const activeIndices = (GRAPH_STATE.activeGraph && GRAPH_STATE.activeNodeMap)
    ? issueIds.map(id => GRAPH_STATE.activeNodeMap.get(id)).filter(idx => idx !== undefined)
    : null;
  const activeMetrics = activeIndices !== null
    ? (activeIndices.length > 0
        ? withSubgraph(activeIndices, sg => {
            const count = sg.nodeCount();
            const heights = sg.criticalPathHeights();
            const bw = count > 500
              ? sg.betweennessApprox(Math.min(100, count))
              : sg.betweenness();
            const heightById = new Map();
            const betweennessById = new Map();
            for (let i = 0; i < count; i++) {
              const id = sg.nodeId(i);
              heightById.set(id, heights[i] ?? 0);
              betweennessById.set(id, bw[i] ?? 0);
            }
            return {
              hasCycles: sg.hasCycles(),
              heightById,
              betweennessById,
            };
          }, GRAPH_STATE.activeGraph)
        : { hasCycles: false, heightById: new Map(), betweennessById: new Map() })
    : null;

  const result = withSubgraph(indices, subgraph => {
    const nodeCount = subgraph.nodeCount();
    const criticalPath = activeMetrics
      ? Float64Array.from({ length: nodeCount }, (_, i) => activeMetrics.heightById.get(subgraph.nodeId(i)) ?? 0)
      : subgraph.criticalPathHeights();
    const betweenness = activeMetrics
      ? Float64Array.from({ length: nodeCount }, (_, i) => activeMetrics.betweennessById.get(subgraph.nodeId(i)) ?? 0)
      : subgraph.betweenness();
    return {
      nodeCount,
      edgeCount: subgraph.edgeCount(),
      pagerank: subgraph.pagerankDefault(),
      betweenness,
      hasCycles: activeMetrics ? activeMetrics.hasCycles : subgraph.hasCycles(),
      criticalPath,
    };
  });

  const elapsed = performance.now() - start;
  console.log(`[Graph] Recalculated metrics in ${elapsed.toFixed(1)}ms`);

  return result;
}

function clampWhatIfResult(result, realCount) {
  if (!result || typeof result !== 'object') return result;
  const unblockedIds = (result.unblocked_ids || []).filter(i => i < realCount);
  const cascadeIds = (result.cascade_ids || []).filter(i => i < realCount);
  result.unblocked_ids = unblockedIds;
  result.cascade_ids = cascadeIds;
  result.direct_unblocks = unblockedIds.length;
  result.transitive_unblocks = cascadeIds.length;
  result.parallel_gain = Math.max(0, unblockedIds.length - 1);
  return result;
}

/**
 * What-if analysis: compute cascade impact of closing an issue
 */
function whatIfClose(issueId) {
  if (!GRAPH_STATE.ready) return null;

  const idx = GRAPH_STATE.nodeMap.get(issueId);
  if (idx === undefined) return null;

  const g = GRAPH_STATE.whatIfGraph || GRAPH_STATE.graph;
  const realCount = GRAPH_STATE.graph.nodeCount();
  const result = GRAPH_STATE.whatIfModel?.hasParentChild
    ? simulateWhatIfCloseIndices(GRAPH_STATE.whatIfModel, [idx])
    : clampWhatIfResult(g.whatIfClose(idx, buildClosedSet()), realCount);

  // Convert node indices back to issue IDs
  if (result) {
    result.unblocked_issue_ids = (result.unblocked_ids || [])
      .map(i => GRAPH_STATE.graph.nodeId(i))
      .filter(Boolean);
    result.cascade_issue_ids = (result.cascade_ids || [])
      .map(i => GRAPH_STATE.graph.nodeId(i))
      .filter(Boolean);
  }

  return result;
}

/**
 * Get top issues by cascade impact
 */
function topWhatIf(limit = 10) {
  if (!GRAPH_STATE.ready || limit <= 0) return [];

  const g = GRAPH_STATE.whatIfGraph || GRAPH_STATE.graph;
  const realCount = GRAPH_STATE.graph.nodeCount();
  const closedSet = buildClosedSet();
  const results = g.topWhatIf(closedSet, limit, buildCandidateSet(true));

  // Enrich with issue IDs
  return (results || [])
    .filter(item => item && item.node < realCount)
    .map(item => {
      const issueId = GRAPH_STATE.graph.nodeId(item.node);
      if (!issueId) return null;
      return {
        ...item,
        issueId,
        isActionable: true,
        result: clampWhatIfResult(item.result, realCount),
      };
    })
    .filter(Boolean);
}

/**
 * Get top issues by cascade impact across ALL open issues (including currently blocked issues)
 */
function allWhatIf(limit = 10) {
  const g = GRAPH_STATE.whatIfGraph || GRAPH_STATE.graph;
  if (!GRAPH_STATE.ready || limit <= 0 || typeof g?.allWhatIf !== 'function') return [];

  try {
    const realCount = GRAPH_STATE.graph.nodeCount();
    const closedSet = buildClosedSet();
    const visibleIds = new Set(execQuery('SELECT id FROM issue_overview_mv').map(r => r.id));

    if (GRAPH_STATE.whatIfModel?.hasParentChild) {
      const evaluated = [];
      for (let node = 0; node < realCount; node++) {
        if (closedSet[node]) continue;
        const issueId = GRAPH_STATE.graph.nodeId(node);
        if (!issueId || !visibleIds.has(issueId)) continue;
        const simResult = simulateWhatIfCloseIndices(GRAPH_STATE.whatIfModel, [node]);
        if (simResult.transitive_unblocks <= 0) continue;
        simResult.unblocked_issue_ids = simResult.unblocked_ids.map(i => GRAPH_STATE.graph.nodeId(i)).filter(Boolean);
        simResult.cascade_issue_ids = simResult.cascade_ids.map(i => GRAPH_STATE.graph.nodeId(i)).filter(Boolean);
        evaluated.push({ node, issueId, result: simResult });
      }
      evaluated.sort((a, b) =>
        (b.result.transitive_unblocks - a.result.transitive_unblocks) ||
        (b.result.direct_unblocks - a.result.direct_unblocks) ||
        (a.node - b.node)
      );
      return evaluated.slice(0, limit).map(item => {
        const issue = getIssue(item.issueId);
        return {
          node: item.node,
          issueId: item.issueId,
          isActionable: Boolean(issue?.is_actionable),
          status: issue?.status || 'open',
          result: item.result,
        };
      });
    }

    const results = g.allWhatIf(closedSet, Math.max(limit * 2 + 2, 22));
    return (results || [])
      .map(item => {
        if (!item || item.node >= realCount) return null;
        const issueId = GRAPH_STATE.graph.nodeId(item.node);
        if (!issueId || !visibleIds.has(issueId)) return null;
        const issue = getIssue(issueId);
        return {
          ...item,
          issueId,
          isActionable: Boolean(issue?.is_actionable),
          status: issue?.status || 'open',
          result: clampWhatIfResult(item.result, realCount),
        };
      })
      .filter(Boolean)
      .slice(0, limit);
  } catch (e) {
    console.warn('[viewer] allWhatIf failed:', e);
    return [];
  }
}

/**
 * Compute combined cascade impact of closing a batch of issues together
 */
function whatIfCloseBatch(issueIds = []) {
  const g = GRAPH_STATE.whatIfGraph || GRAPH_STATE.graph;
  if (!GRAPH_STATE.ready || !Array.isArray(issueIds) || issueIds.length === 0) return null;
  if (!GRAPH_STATE.whatIfModel?.hasParentChild && typeof g?.whatIfCloseBatch !== 'function') return null;

  try {
    const realCount = GRAPH_STATE.graph.nodeCount();
    const indices = issueIds
      .map(id => GRAPH_STATE.nodeMap.get(id))
      .filter(idx => idx !== undefined && idx < realCount);
    if (indices.length === 0) return null;

    const result = GRAPH_STATE.whatIfModel?.hasParentChild
      ? simulateWhatIfCloseIndices(GRAPH_STATE.whatIfModel, indices)
      : clampWhatIfResult(g.whatIfCloseBatch(new Uint32Array(indices), buildClosedSet()), realCount);
    if (result) {
      result.unblocked_issue_ids = (result.unblocked_ids || [])
        .map(i => GRAPH_STATE.graph.nodeId(i))
        .filter(Boolean);
      result.cascade_issue_ids = (result.cascade_ids || [])
        .map(i => GRAPH_STATE.graph.nodeId(i))
        .filter(Boolean);
    }
    return result;
  } catch (e) {
    console.warn('[viewer] whatIfCloseBatch failed:', e);
    return null;
  }
}

/**
 * Get parallelism unlockers from WASM parallelCutFiltered (fallback to parallelCutSuggestions / parallelCutDefault)
 */
function getParallelCutData(limit = 10) {
  if (!GRAPH_STATE.ready || limit <= 0) {
    return { items: [], open_nodes: 0, current_actionable: 0 };
  }

  try {
    const g = GRAPH_STATE.whatIfGraph || GRAPH_STATE.graph;
    const realCount = GRAPH_STATE.graph.nodeCount();
    const closedSet = buildClosedSet();
    let lifecycleOpenCount = 0;
    for (let i = 0; i < realCount; i++) {
      if (!closedSet[i]) lifecycleOpenCount++;
    }
    const raw = typeof g.parallelCutFiltered === 'function'
      ? g.parallelCutFiltered(closedSet, limit, buildCandidateSet(true))
      : ((limit === 10 && typeof g.parallelCutDefault === 'function')
          ? g.parallelCutDefault(closedSet)
          : (typeof g.parallelCutSuggestions === 'function'
              ? g.parallelCutSuggestions(closedSet, limit)
              : g.parallelCutDefault?.(closedSet)));
    const visibleIds = new Set(execQuery('SELECT id FROM issue_overview_mv').map(r => r.id));
    const items = (raw?.items || [])
      .map(item => {
        if (!item || item.node >= realCount) return null;
        const issueId = GRAPH_STATE.graph.nodeId(item.node);
        if (!issueId || !visibleIds.has(issueId)) return null;
        const issue = getIssue(issueId);
        return {
          ...item,
          issueId,
          title: issue?.title || issueId,
          status: issue?.status || 'open',
          priority: issue?.priority ?? 2,
          parallel_gain: Number(item.parallel_gain ?? 0),
          new_actionable: Number(item.new_actionable ?? 0),
        };
      })
      .filter(Boolean)
      .slice(0, limit);
    return {
      items,
      open_nodes: lifecycleOpenCount,
      current_actionable: Number(raw?.current_actionable ?? 0),
    };
  } catch (e) {
    console.warn('[viewer] getParallelCutData failed:', e);
    return { items: [], open_nodes: 0, current_actionable: 0 };
  }
}

/**
 * Get Tarjan bridge cut-edges on the active subgraph (critical single-link dependency bottlenecks)
 */
function getBridgeDependencies(limit = 20) {
  const activeGraph = GRAPH_STATE.activeGraph || GRAPH_STATE.graph;
  if (!GRAPH_STATE.ready || limit <= 0 || typeof activeGraph?.bridges !== 'function') return [];

  try {
    const rawBridges = activeGraph.bridges();
    if (!Array.isArray(rawBridges) || rawBridges.length === 0) return [];

    const resolvedIds = getResolvedIssueIDs();
    const directedEdges = new Set(
      execQuery(`
        SELECT issue_id, depends_on_id
        FROM dependencies
        WHERE COALESCE(type, 'blocks') IN ${BLOCKING_DEPENDENCY_TYPES_SQL}
      `)
        .filter(r => !resolvedIds.has(r.issue_id) && !resolvedIds.has(r.depends_on_id))
        .map(r => `${r.issue_id}\0${r.depends_on_id}`)
    );

    const bridges = [];
    for (const pair of rawBridges) {
      if (!Array.isArray(pair) || pair.length < 2) continue;
      const idA = activeGraph.nodeId(pair[0]);
      const idB = activeGraph.nodeId(pair[1]);
      if (!idA || !idB || resolvedIds.has(idA) || resolvedIds.has(idB)) continue;

      let from = idA;
      let to = idB;
      if (!directedEdges.has(`${idA}\0${idB}`) && directedEdges.has(`${idB}\0${idA}`)) {
        from = idB;
        to = idA;
      }

      const fromIssue = getIssue(from);
      const toIssue = getIssue(to);
      bridges.push({
        from,
        to,
        fromTitle: fromIssue?.title || from,
        toTitle: toIssue?.title || to,
        fromAvailable: Boolean(fromIssue),
        toAvailable: Boolean(toIssue),
      });
    }

    bridges.sort((a, b) => a.from.localeCompare(b.from) || a.to.localeCompare(b.to));
    return bridges.slice(0, limit);
  } catch (e) {
    console.warn('[viewer] getBridgeDependencies failed:', e);
    return [];
  }
}

/**
 * Format critical-path hop count (edges = max(0, nodeCount - 1))
 */
function formatCriticalPathHops(entry) {
  const explicitHops = Number(entry?.hops);
  const hops = Number.isFinite(explicitHops) && explicitHops >= 0
    ? explicitHops
    : Math.max(0, Number(entry?.length ?? entry?.path?.length ?? 0) - 1);
  return `${hops} ${hops === 1 ? 'hop' : 'hops'}`;
}

/**
 * Get top K critical paths from WASM kCriticalPaths(k) (empty when active cycles exist)
 */
function getTopCriticalPaths(k = 5) {
  const activeGraph = GRAPH_STATE.activeGraph || GRAPH_STATE.graph;
  if (!GRAPH_STATE.ready || !activeGraph || k <= 0 || activeGraph.hasCycles()) return [];

  try {
    if (typeof activeGraph.kCriticalPaths === 'function') {
      const res = activeGraph.kCriticalPaths(k);
      if (res && Array.isArray(res.paths) && res.paths.length > 0) {
        return res.paths
          .map(p => {
            const path = (p.nodes || []).map(idx => activeGraph.nodeId(idx)).filter(Boolean);
            const nodeCount = Number(p.length ?? path.length);
            return {
              path,
              length: nodeCount,
              hops: Math.max(0, nodeCount - 1),
              label: path.join(' → '),
            };
          })
          .filter(p => p.path.length > 0);
      }
    }
    const single = getCriticalPathSequence();
    if (single && Array.isArray(single.path) && single.path.length > 0) {
      const nodeCount = Number(single.length || single.path.length);
      return [{
        path: single.path,
        length: nodeCount,
        hops: Math.max(0, nodeCount - 1),
        label: single.path.join(' → '),
      }];
    }
    return [];
  } catch (e) {
    console.warn('[viewer] getTopCriticalPaths failed:', e);
    return [];
  }
}

/**
 * Get actual issues eligible under the exported full-source readiness policy.
 */
function getActionableIssues() {
  if (!GRAPH_STATE.ready) return [];

  return execQuery('SELECT id FROM issue_overview_mv WHERE is_actionable = 1 ORDER BY id')
    .map(row => row.id);
}

/**
 * Find cycle break suggestions
 */
function getCycleBreakSuggestions(limit = 5) {
  const activeGraph = GRAPH_STATE.activeGraph || GRAPH_STATE.graph;
  if (!GRAPH_STATE.ready || !activeGraph) return null;

  const result = activeGraph.cycleBreakSuggestions(limit, 100);
  return result;
}

/**
 * Get greedy top-k issues to maximize unblocks
 */
function getTopKSet(k = 5) {
  if (!GRAPH_STATE.ready) return null;

  const g = GRAPH_STATE.whatIfGraph || GRAPH_STATE.graph;
  const realCount = GRAPH_STATE.graph.nodeCount();
  const closedSet = buildClosedSet();
  const result = g.topkSet(closedSet, k, buildCandidateSet({ lifecycleOnly: true }));

  if (result && k > 0) {
    const actionableCandidates = buildCandidateSet(true);
    let actionableCount = 0;
    for (let i = 0; i < realCount; i++) {
      if (!closedSet[i] && actionableCandidates[i]) actionableCount++;
    }
    result.open_nodes = actionableCount;
  }

  // Enrich with issue IDs
  if (result && result.items) {
    result.items = result.items
      .filter(item => item && item.node < realCount)
      .map(item => {
        const issueId = GRAPH_STATE.graph.nodeId(item.node);
        if (!issueId) return null;
        return {
          ...item,
          issueId,
          unblocked_issue_ids: (item.unblocked_ids || [])
            .filter(i => i < realCount)
            .map(i => GRAPH_STATE.graph.nodeId(i))
            .filter(Boolean),
        };
      })
      .filter(Boolean);
  }

  return result;
}

/**
 * Compute greedy dependency vertex cover from the WASM graph engine
 */
function getCoverageSet(limit = 10) {
  if (!GRAPH_STATE.ready) return null;
  if (!GRAPH_STATE.activeGraph || GRAPH_STATE.activeGraph.edgeCount() === 0) {
    return {
      items: [],
      edgesCovered: 0,
      totalEdges: 0,
      coverageRatio: 1,
      edges_covered: 0,
      total_edges: 0,
      coverage_ratio: 1,
    };
  }
  const graph = GRAPH_STATE.activeGraph;

  try {
    const raw = (limit === 10 && typeof graph.coverageSetDefault === 'function')
      ? graph.coverageSetDefault()
      : graph.coverageSet?.(limit);
    if (!raw || !Array.isArray(raw.items)) {
      return {
        items: [],
        edgesCovered: 0,
        totalEdges: 0,
        coverageRatio: 1,
        edges_covered: 0,
        total_edges: 0,
        coverage_ratio: 1,
      };
    }

    const ids = raw.items
      .map(item => graph.nodeId(item.node))
      .filter(Boolean);
    const issueById = new Map();
    if (ids.length > 0) {
      const placeholders = ids.map(() => '?').join(',');
      const rows = execQuery(
        `SELECT id, title, status, priority FROM issue_overview_mv WHERE id IN (${placeholders})`,
        ids
      );
      for (const row of rows) {
        issueById.set(row.id, row);
      }
    }

    const items = raw.items
      .map(item => {
        const issueId = graph.nodeId(item.node);
        const row = issueById.get(issueId);
        if (!issueId || !row) return null;
        return {
          ...item,
          issueId,
          title: row.title || issueId,
          status: row.status || 'open',
          priority: row.priority ?? 2,
        };
      })
      .filter(Boolean);

    const edgesCovered = Number(raw.edges_covered ?? raw.edgesCovered ?? 0);
    const totalEdges = Number(raw.total_edges ?? raw.totalEdges ?? 0);
    const coverageRatio = Number(raw.coverage_ratio ?? raw.coverageRatio ?? (totalEdges === 0 ? 1 : 0));

    return {
      ...raw,
      items,
      edgesCovered,
      totalEdges,
      coverageRatio,
      edges_covered: edgesCovered,
      total_edges: totalEdges,
      coverage_ratio: coverageRatio,
    };
  } catch (e) {
    console.warn('[viewer] getCoverageSet failed:', e);
    return {
      items: [],
      edgesCovered: 0,
      totalEdges: 0,
      coverageRatio: 0,
      edges_covered: 0,
      total_edges: 0,
      coverage_ratio: 0,
    };
  }
}

// ============================================================================
// Query Layer - Using materialized views for performance
// ============================================================================

function normalizeSearchText(raw) {
  return String(raw ?? '').normalize('NFC');
}

function escapeLikePattern(raw) {
  return String(raw ?? '').replace(/[\\%_]/g, '\\$&');
}

function extractSearchWordParts(raw) {
  const text = normalizeSearchText(raw).trim();
  if (!text) return [];
  const matches = text.match(/[\p{L}\p{M}\p{N}_]+/gu);
  if (!matches) return [];
  return matches.filter(part => /[\p{L}\p{N}]/u.test(part));
}

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

  // Readiness comes from the full-source export snapshot, not visible edges.
  if (filters.hasBlockers === true || filters.hasBlockers === 'true') {
    clauses.push(`(${col('dependency_state')} <> 'satisfied' OR ${col('status')} = 'blocked')`);
  } else if (filters.hasBlockers === false || filters.hasBlockers === 'false') {
    clauses.push(`${col('is_actionable')} = 1`);
  }

  // Blocking filter (has items depending on it)
  if (filters.isBlocking === true || filters.isBlocking === 'true') {
    clauses.push(`${col('blocks_count')} > 0`);
  }

  // Critical path filter
  if (filters.criticalPath === true || filters.criticalPath === 'true') {
    const cpIds = Array.isArray(filters.criticalPathIds)
      ? filters.criticalPathIds
      : (getCriticalPathSequence()?.path || []);
    if (cpIds.length === 0) {
      clauses.push('1 = 0');
    } else {
      clauses.push(`${col('id')} IN (${cpIds.map(() => '?').join(',')})`);
      params.push(...cpIds);
    }
  }

  // Label filter (JSON array contains; exact json_each match with Go-escaped case-sensitive INSTR fallback)
  if (filters.labels?.length) {
    const labels = Array.isArray(filters.labels) ? filters.labels : [filters.labels];
    if (supportsJsonEach()) {
      const labelClauses = labels.map(() => `EXISTS (SELECT 1 FROM json_each(COALESCE(${col('labels')}, '[]')) WHERE value = ?)`);
      clauses.push(`(${labelClauses.join(' OR ')})`);
      params.push(...labels.map(l => String(l)));
    } else if (ensureSqlFunctions(DB_STATE.db)) {
      const labelClauses = labels.map(() => `bv_has_label(COALESCE(${col('labels')}, '[]'), ?) = 1`);
      clauses.push(`(${labelClauses.join(' OR ')})`);
      params.push(...labels.map(l => String(l)));
    } else {
      const labelClauses = [];
      const labelsExpr = `COALESCE(${col('labels')}, '[]')`;
      for (const rawLabel of labels) {
        const rawToken = JSON.stringify(String(rawLabel));
        const goToken = rawToken
          .replace(/</g, '\\u003c')
          .replace(/>/g, '\\u003e')
          .replace(/&/g, '\\u0026');
        if (goToken !== rawToken) {
          labelClauses.push(`(INSTR(${labelsExpr}, ?) > 0 OR INSTR(${labelsExpr}, ?) > 0)`);
          params.push(goToken, rawToken);
        } else {
          labelClauses.push(`INSTR(${labelsExpr}, ?) > 0`);
          params.push(goToken);
        }
      }
      clauses.push(`(${labelClauses.join(' OR ')})`);
    }
  }

  // Search filter (LIKE-based, FTS5 handled separately)
  if (filters.search) {
    const searchParts = normalizeSearchText(filters.search).trim().split(/\s+/).filter(Boolean);
    const searchCols = [
      col('title'),
      col('description'),
      col('id'),
      `COALESCE(${col('labels')}, '')`,
      `COALESCE(${col('assignee')}, '')`,
      `COALESCE(${col('notes')}, '')`,
      `COALESCE(${col('design')}, '')`,
      `COALESCE(${col('acceptance_criteria')}, '')`,
      `COALESCE(${col('external_ref')}, '')`,
    ];
    for (const part of searchParts) {
      const nfcPattern = `%${escapeLikePattern(part)}%`;
      const nfdPart = part.normalize('NFD');
      if (nfdPart !== part) {
        const nfdPattern = `%${escapeLikePattern(nfdPart)}%`;
        clauses.push(`(${searchCols.map(c => `${c} LIKE ? ESCAPE '\\' OR ${c} LIKE ? ESCAPE '\\'`).join(' OR ')})`);
        for (let i = 0; i < searchCols.length; i++) {
          params.push(nfcPattern, nfdPattern);
        }
      } else {
        clauses.push(`(${searchCols.map(c => `${c} LIKE ? ESCAPE '\\'`).join(' OR ')})`);
        for (let i = 0; i < searchCols.length; i++) {
          params.push(nfcPattern);
        }
      }
    }
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
    'priority': 'priority ASC, COALESCE(triage_score, 0) DESC',
    'updated': 'updated_at DESC',
    'score': 'COALESCE(triage_score, 0) DESC',
    'blocks': 'blocks_count DESC',
    'created': 'created_at DESC',
    'title': 'title ASC',
    'id': 'id ASC',
  };
  const rawSearch = typeof filters.search === 'string' ? normalizeSearchText(filters.search).trim() : '';
  if (rawSearch && sort === 'score') {
    const likeTerm = `%${escapeLikePattern(rawSearch)}%`;
    sql += ` ORDER BY CASE WHEN LOWER(id) = LOWER(?) THEN 3 WHEN LOWER(title) LIKE LOWER(?) ESCAPE '\\' THEN 2 ELSE 1 END DESC, COALESCE(triage_score, 0) DESC`;
    params.push(rawSearch, likeTerm);
  } else {
    sql += ` ORDER BY ${sortMap[sort] || sortMap.priority}`;
  }
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

function parseJsonArraySafe(raw) {
  if (!raw) return [];
  try {
    const parsed = JSON.parse(raw);
    return Array.isArray(parsed) ? parsed : [];
  } catch {
    return [];
  }
}

function parseJsonObjectSafe(raw) {
  if (!raw) return null;
  try {
    const parsed = JSON.parse(raw);
    return parsed && typeof parsed === 'object' && !Array.isArray(parsed) ? parsed : null;
  } catch {
    return null;
  }
}

function isIssueOverdue(issue) {
  if (!issue || !issue.due_date) return false;
  const st = String(issue.status || '').trim().toLowerCase();
  if (st === 'closed' || st === 'tombstone') return false;
  const due = new Date(issue.due_date);
  return !Number.isNaN(due.getTime()) && due.getTime() < getReferenceNowMs();
}

function isIssueInCycle(id) {
  const activeGraph = GRAPH_STATE.activeGraph || GRAPH_STATE.graph;
  const activeMap = GRAPH_STATE.activeNodeMap || GRAPH_STATE.nodeMap;
  if (!id || !GRAPH_STATE.ready || !activeGraph || !activeMap) return false;
  try {
    if (!activeGraph.hasCycles()) return false;
    const idx = activeMap.get(id);
    if (idx === undefined) return false;
    const enumRes = activeGraph.enumerateCycles?.(100);
    if (!Array.isArray(enumRes?.cycles)) return false;
    return enumRes.cycles.some(cycle => Array.from(cycle).includes(idx));
  } catch {
    return false;
  }
}

function getTriageRecommendation(id) {
  try {
    const rows = execQuery(`
      SELECT score,
             action,
             reasons AS reasons_json,
             unblocks_ids AS unblocks_ids_json,
             breakdown AS breakdown_json
      FROM triage_recommendations
      WHERE issue_id = ?
    `, [id]);
    return rows[0] || null;
  } catch {
    return null;
  }
}

/**
 * Get a single issue by ID
 */
function getIssue(id) {
  const results = execQuery(`SELECT * FROM issue_overview_mv WHERE id = ?`, [id]);
  const issue = results[0] || null;
  if (issue) {
    issue.comments = getIssueComments(id);
    const rec = getTriageRecommendation(id);
    if (rec) {
      const reasons = parseJsonArraySafe(rec.reasons_json);
      const unblocksIds = parseJsonArraySafe(rec.unblocks_ids_json);
      const breakdown = parseJsonObjectSafe(rec.breakdown_json);
      issue.triage_rec_score = rec.score;
      issue.triage_action = rec.action || '';
      issue.triage_reasons = reasons;
      issue.triage_unblocks_ids = unblocksIds;
      issue.triage_breakdown = breakdown;
      issue.reason = reasons[0] || rec.action || '';
      issue.score_breakdown_bars = getScoreBreakdownBars(breakdown);
    } else {
      issue.triage_action = '';
      issue.triage_reasons = [];
      issue.triage_unblocks_ids = [];
      issue.triage_breakdown = null;
      issue.score_breakdown_bars = [];
    }
    const depGroups = getIssueDependencyGroups(id);
    issue.dependencyGroups = depGroups;
    issue.has_any_dependencies = Boolean(
      issue.blocks_ids ||
      issue.blocked_by_ids ||
      depGroups.blocks.length ||
      depGroups.blockedBy.length ||
      depGroups.parents.length ||
      depGroups.children.length ||
      depGroups.related.length
    );
    issue.in_cycle = Boolean(issue.in_cycle || isIssueInCycle(id));
    issue.is_overdue = isIssueOverdue(issue);
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
  // counts and dependency_state the issues view does — closing a blocker drops
  // its dependents' blocker_count to 0 here too, which getNodeColor uses to
  // derive the effective "blocked" colouring (bv-issue#143/#144).
  const mvCols = getOverviewColumnSet();
  const extraCols = [
    'defer_until',
    'is_template',
    'pinned',
    'ephemeral',
    'pagerank',
    'betweenness',
    'eigenvector',
    'critical_path_depth',
    'in_cycle',
  ].filter(c => mvCols.has(c));
  const extraSelect = extraCols.length > 0 ? `, ${extraCols.join(', ')}` : '';
  const issues = execQuery(`
    SELECT id, title, description, status, priority, issue_type, assignee, labels,
           created_at, updated_at, closed_at, blocker_count, dependent_count,
           dependency_state, is_actionable${extraSelect}
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
    closed_at: row.closed_at,
    defer_until: row.defer_until ?? null,
    is_template: isTruthyLifecycleFlag(row.is_template),
    pinned: isTruthyLifecycleFlag(row.pinned),
    ephemeral: isTruthyLifecycleFlag(row.ephemeral),
    pagerank: Number.isFinite(Number(row.pagerank)) ? Number(row.pagerank) : 0,
    betweenness: Number.isFinite(Number(row.betweenness)) ? Number(row.betweenness) : 0,
    eigenvector: Number.isFinite(Number(row.eigenvector)) ? Number(row.eigenvector) : 0,
    critical_path_depth: Number.isFinite(Number(row.critical_path_depth)) ? Number(row.critical_path_depth) : 0,
    in_cycle: Boolean(row.in_cycle),
    // Pre-computed active-blocker / active-dependent counts from the
    // materialized view (which excludes closed/tombstoned endpoints).
    blocker_count: row.blocker_count ?? 0,
    dependent_count: row.dependent_count ?? 0,
    dependency_state: row.dependency_state || 'satisfied',
    is_actionable: Boolean(row.is_actionable),
  }));

  const dependencies = execQuery(`
    SELECT issue_id, depends_on_id, type
    FROM dependencies
  `);

  return { issues, dependencies };
}

/**
 * Full-text search using FTS5 (if available)
 */
const BM25_WEIGHTS = '3.0, 2.0, 1.0, 1.5, 0.5, 0.8, 0.8, 0.8, 1.0'; // id, title, description, labels, assignee, notes, design, acceptance_criteria, external_ref
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
  return extractSearchWordParts(term).length;
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

/**
 * Build a safe FTS5 MATCH expression from arbitrary user input.
 * Normalizes input to NFC, extracts Unicode letter/mark/number/underscore
 * words containing at least one letter or number, and wraps each as a quoted
 * prefix phrase ("word"*) so hyphens (bv-123, GH-42, obsidian-note-probe),
 * combining marks (NFD "nai\u0308ve", NFC "हिंदी"), colons, punctuation, and
 * FTS5 boolean operators (AND/OR/NOT/NEAR) never trigger SQLite FTS5 errors,
 * while underscore-only ("___") or punctuation-only queries return "" to fall
 * back cleanly to substring LIKE matching.
 */
function buildFtsMatchQuery(raw) {
  const parts = extractSearchWordParts(raw);
  if (parts.length === 0) return '';
  return parts.map(part => `"${part.replace(/"/g, '""')}"*`).join(' ');
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

  const ftsQuery = buildFtsMatchQuery(term);
  const { clauses, params } = buildFilterClauses(searchFilters, 'i');
  const baseSQL = `
      SELECT i.*,
             snippet(issues_fts, -1, '<mark>', '</mark>', '...', 32) as snippet,
             ${BM25_EXPR} as bm25_score
      FROM issues_fts
      JOIN issue_overview_mv i ON issues_fts.id = i.id
      WHERE issues_fts MATCH ?
    `;
  let sql = baseSQL;
  const queryParams = [ftsQuery];
  if (clauses.length > 0) {
    sql += ` AND ${clauses.join(' AND ')}`;
    queryParams.push(...params);
  }

  let fetchLimit = limit;
  const minCandidates = isShortQuery(term) ? HYBRID_CANDIDATE_MIN_SHORT : HYBRID_CANDIDATE_MIN;
  if (mode === 'hybrid') {
    fetchLimit = Math.max(limit * 2, offset + limit, minCandidates);
  }
  const fetchOffset = mode === 'hybrid' ? 0 : offset;
  sql += ` ORDER BY ${BM25_EXPR} LIMIT ? OFFSET ?`;
  queryParams.push(fetchLimit, fetchOffset);

  const UNBOUNDED_SUBSTRING_POOL_LIMIT = 1000000;
  let rows = [];
  let usedFTS = false;
  if (supportsFTS5() && ftsQuery) {
    try {
      rows = execQuery(sql, queryParams);
      usedFTS = true;
    } catch {
      // Invalid FTS syntax can still use plain substring matching. Fetch all
      // matching rows before lexical/exact-ID/hybrid scoring so low-triage or
      // closed exact-ID/title matches are never truncated at 200 rows.
      rows = queryIssues({ ...searchFilters, search: term }, 'score', UNBOUNDED_SUBSTRING_POOL_LIMIT, 0);
    }
  } else {
    rows = queryIssues({ ...searchFilters, search: term }, 'score', UNBOUNDED_SUBSTRING_POOL_LIMIT, 0);
  }

  if (mode !== 'hybrid' || typeof HybridScorer === 'undefined' || typeof HYBRID_PRESETS === 'undefined') {
    if (usedFTS) {
      const bm25Rows = rows.filter(r => r.bm25_score !== undefined && r.bm25_score !== null && Number.isFinite(Number(r.bm25_score)));
      const maxBM25Text = bm25Rows.length > 0
        ? Math.max(...bm25Rows.map(r => Math.abs(Number(r.bm25_score))), 0)
        : 0;
      rows = rows.map(r => {
        const hasBM25 = r.bm25_score !== undefined && r.bm25_score !== null && Number.isFinite(Number(r.bm25_score));
        return {
          ...r,
          text_score: hasBM25
            ? (maxBM25Text > 0 ? Math.min(Math.abs(Number(r.bm25_score)) / maxBM25Text, 1) : 1.0)
            : computeLexicalTextScore(r, term),
        };
      });
      if (isLikelyIssueID(term)) {
        rows = promoteExactID(term, rows);
      }
      return rows.slice(0, limit);
    }

    rows = rows
      .map(r => ({ ...r, text_score: computeLexicalTextScore(r, term) }))
      .sort((a, b) =>
        (b.text_score - a.text_score) ||
        ((a.priority ?? 2) - (b.priority ?? 2)) ||
        String(a.id || '').localeCompare(String(b.id || ''))
      );
    if (isLikelyIssueID(term)) {
      rows = promoteExactID(term, rows);
    }
    return rows.slice(offset, offset + limit);
  }

  const refNowMs = getReferenceNowMs();
  const bm25Rows = rows.filter(r => r.bm25_score !== undefined && r.bm25_score !== null && Number.isFinite(Number(r.bm25_score)));
  const maxBM25 = bm25Rows.length > 0
    ? Math.max(...bm25Rows.map(r => Math.abs(Number(r.bm25_score))), 0)
    : 0;
  const { hasMetrics, maxPageRank } = getPageRankBounds();
  const maxBlockerCount = getMaxBlocksCount();
  const normalized = rows.map(r => {
    const hasBM25 = r.bm25_score !== undefined && r.bm25_score !== null && Number.isFinite(Number(r.bm25_score));
    const textScore = hasBM25
      ? (maxBM25 > 0 ? Math.min(Math.abs(Number(r.bm25_score)) / maxBM25, 1) : 1.0)
      : computeLexicalTextScore(r, term);
    const normalizedPageRank = typeof normalizeHybridPageRank === 'function'
      ? normalizeHybridPageRank(r.pagerank, hasMetrics, maxPageRank)
      : (hasMetrics
        ? (maxPageRank > 0 ? Math.min(1, Math.max(0, (Number(r.pagerank) || 0) / maxPageRank)) : 0)
        : 0.5);
    const normStatus = String(r.status || '').trim().toLowerCase();
    const isClosedOrTombstone = normStatus === 'closed' || normStatus === 'tombstone';
    const isTemplateOrPinned =
      r.is_template === true || r.is_template === 'true' || Number(r.is_template) > 0 ||
      r.pinned === true || r.pinned === 'true' || Number(r.pinned) > 0 ||
      r.ephemeral === true || r.ephemeral === 'true' || Number(r.ephemeral) > 0 ||
      normStatus === 'pinned';
    const deferUntilMs = r.defer_until ? Date.parse(r.defer_until) : NaN;
    const isFutureDeferred = (normStatus === 'open' || normStatus === 'in_progress') &&
      Number.isFinite(deferUntilMs) && deferUntilMs > refNowMs;
    const isDeferredEffective = !isClosedOrTombstone && (isTemplateOrPinned || isFutureDeferred);
    const isDepBlocked = !isClosedOrTombstone && !isDeferredEffective &&
      (normStatus === 'open' || normStatus === 'in_progress') &&
      ((r.dependency_state && String(r.dependency_state).trim().toLowerCase() !== 'satisfied') ||
        Number(r.open_blocker_count ?? r.blocker_count ?? r.blocked_by_count ?? 0) > 0);
    const scoringStatus = isDeferredEffective ? 'deferred' : (isDepBlocked ? 'blocked' : normStatus);
    return {
      ...r,
      _displayStatus: r.status,
      textScore,
      blockerCount: r.blocks_count ?? r.dependent_count ?? 0,
      updatedAt: r.updated_at,
      pagerank: normalizedPageRank,
      status: scoringStatus,
      priority: r.priority,
    };
  });

  const baseWeights = HYBRID_PRESETS[preset] || HYBRID_PRESETS.default;
  const weights = adjustHybridWeightsForQuery(baseWeights, term);
  const scorerOptions = { maxBlockerCount };
  let ranked = null;
  if (typeof window.scoreBatchHybrid === 'function') {
    ranked = window.scoreBatchHybrid(normalized, weights, scorerOptions);
  }
  if (!Array.isArray(ranked)) {
    const scorer = new HybridScorer(weights, scorerOptions);
    ranked = scorer.scoreAndRank(normalized);
  }
  if (isLikelyIssueID(term)) {
    ranked = promoteExactID(term, ranked);
  }

  return ranked
    .slice(offset, offset + limit)
    .map(({ _displayStatus, ...r }) => ({
      ...r,
      status: _displayStatus ?? r.status,
      text_score: r.textScore,
    }));
}

/**
 * Compute deterministic lexical relevance score in (0, 1] when FTS5 BM25 is unavailable.
 */
function computeLexicalTextScore(row, query) {
  const q = normalizeSearchText(query).trim().toLowerCase();
  if (!q) return 0.5;
  const wordParts = extractSearchWordParts(q);
  const terms = wordParts.length > 0 ? wordParts : q.split(/\s+/).filter(Boolean);
  if (terms.length === 0) return 0.5;

  const id = normalizeSearchText(row.id).toLowerCase();
  const title = normalizeSearchText(row.title).toLowerCase();
  const labels = normalizeSearchText(row.labels).toLowerCase();
  const assignee = normalizeSearchText(row.assignee).toLowerCase();
  const description = normalizeSearchText(row.description).toLowerCase();
  const extraText = normalizeSearchText(
    [row.notes, row.design, row.acceptance_criteria, row.external_ref]
      .filter(Boolean)
      .join(' ')
  ).toLowerCase();

  if (id === q || title === q) return 1.0;

  let baseScore = 0.35;
  let hasTitleOrIdMatch = false;
  if (id.startsWith(q) || title.startsWith(q)) {
    baseScore = 0.95;
    hasTitleOrIdMatch = true;
  } else if (title.includes(q)) {
    baseScore = 0.9;
    hasTitleOrIdMatch = true;
  } else {
    const titleMatches = terms.filter(t => title.includes(t)).length;
    if (titleMatches > 0) {
      const titleWords = extractSearchWordParts(title);
      const prefixHits = terms.filter(t => titleWords.some(w => w.startsWith(t))).length;
      const score = 0.75 + 0.15 * (titleMatches / terms.length) + 0.05 * (prefixHits / terms.length);
      baseScore = Math.min(0.95, Math.max(0.75, score));
      hasTitleOrIdMatch = true;
    } else if (terms.some(t => id.includes(t) || labels.includes(t) || assignee.includes(t))) {
      baseScore = 0.65;
      hasTitleOrIdMatch = terms.some(t => id.includes(t));
    } else {
      const descMatches = terms.filter(t => description.includes(t)).length;
      if (descMatches > 0) {
        baseScore = Math.min(0.55, 0.45 + 0.10 * (descMatches / terms.length));
      } else if (extraText) {
        const extraMatches = terms.filter(t => extraText.includes(t)).length;
        if (extraMatches > 0) {
          baseScore = Math.min(0.45, 0.40 + 0.05 * (extraMatches / terms.length));
        }
      }
    }
  }

  // Apply +0.35 shortQueryLexicalBoost on short queries (<=2 tokens or <=12 chars)
  // when every query token matches across searchable fields, keeping non-title/ID
  // matches strictly below exact/title matches (capped at 0.92 vs 1.0).
  if (isShortQuery(q)) {
    const searchableAll = [id, title, labels, assignee, description, extraText].join(' ');
    if (terms.every(t => searchableAll.includes(t))) {
      return Math.min(hasTitleOrIdMatch ? 1.0 : 0.92, baseScore + 0.35);
    }
  }

  return baseScore;
}

/**
 * Inspect global PageRank normalization bounds across exported issue metrics,
 * preferring active (non-closed, non-tombstone) max PageRank so historical hubs
 * never compress active scores toward zero (matching Go AnalyzerMetricsLoader).
 */
function getPageRankBounds() {
  try {
    const rows = execQuery(`
      SELECT COUNT(*) AS count,
             MAX(CASE WHEN LOWER(i.status) NOT IN ('closed', 'tombstone') THEN m.pagerank ELSE 0 END) AS max_active_pr,
             MAX(m.pagerank) AS max_any_pr
      FROM issue_metrics m
      JOIN issues i ON i.id = m.issue_id
    `);
    const row = rows?.[0];
    const count = Number(row?.count || 0);
    const chosenMax = typeof resolveHybridPageRankMax === 'function'
      ? resolveHybridPageRankMax(row?.max_active_pr, row?.max_any_pr)
      : ((Number.isFinite(Number(row?.max_active_pr)) && Number(row?.max_active_pr) > 0)
        ? Number(row?.max_active_pr)
        : (Number.isFinite(Number(row?.max_any_pr)) && Number(row?.max_any_pr) > 0 ? Number(row?.max_any_pr) : 0));
    return { hasMetrics: count > 0, maxPageRank: chosenMax };
  } catch {
    try {
      const rows = execQuery('SELECT COUNT(*) AS count, MAX(pagerank) AS max_pr FROM issue_metrics');
      const row = rows?.[0];
      const count = Number(row?.count || 0);
      const maxPR = Number(row?.max_pr || 0);
      return { hasMetrics: count > 0, maxPageRank: Number.isFinite(maxPR) && maxPR > 0 ? maxPR : 0 };
    } catch {
      return { hasMetrics: false, maxPageRank: 0 };
    }
  }
}

/**
 * Inspect corpus-global maximum downstream blocks_count so hybrid impact
 * normalization matches Go's corpus-wide maxBlockerCount rather than subset max.
 */
function getMaxBlocksCount() {
  try {
    const rows = execQuery('SELECT MAX(COALESCE(blocks_count, dependent_count, 0)) AS max_blocks FROM issue_overview_mv');
    const maxBlocks = Number(rows?.[0]?.max_blocks || 0);
    return Number.isFinite(maxBlocks) && maxBlocks > 0 ? maxBlocks : 0;
  } catch {
    return 0;
  }
}

function countSearchIssues(term, filters = {}) {
  const searchFilters = { ...filters };
  delete searchFilters.search;
  const ftsQuery = buildFtsMatchQuery(term);
  if (!supportsFTS5() || !ftsQuery) return countIssues({ ...searchFilters, search: term });
  const { clauses, params } = buildFilterClauses(searchFilters, 'i');

  let sql = `
      SELECT COUNT(*) as count
      FROM issues_fts
      JOIN issue_overview_mv i ON issues_fts.id = i.id
      WHERE issues_fts MATCH ?
    `;
  const queryParams = [ftsQuery];
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
  const stats = { open: 0, in_progress: 0, closed: 0, tombstone: 0 };
  let mvTombstones = 0;

  try {
    // Count visible lifecycle statuses from issue_overview_mv (which excludes tombstones)
    const statusCounts = execQuery(`
      SELECT status, COUNT(*) as count
      FROM issue_overview_mv
      GROUP BY status
    `);
    console.log('[Stats] Status counts:', statusCounts);
    statusCounts.forEach(row => {
      if (row.status === 'tombstone') {
        mvTombstones = row.count;
      } else {
        stats[row.status] = row.count;
      }
    });
    try {
      stats.tombstone = execScalar(`SELECT COUNT(*) FROM issues WHERE status = 'tombstone'`) ?? mvTombstones;
    } catch {
      stats.tombstone = mvTombstones;
    }
    console.log('[Stats] Parsed stats:', stats);
  } catch (err) {
    console.error('[Stats] Error loading status counts:', err);
  }

  // Explicit 'blocked' lifecycle status plus dependency-blocked open/in_progress issues.
  stats.blocked = execScalar(`
    SELECT COUNT(*) FROM issue_overview_mv
    WHERE status = 'blocked'
    OR (dependency_state <> 'satisfied' AND status IN ('open', 'in_progress'))
  `) || 0;

  // Planning readiness includes deferral and lifecycle at the export clock.
  stats.actionable = execScalar(`
    SELECT COUNT(*) FROM issue_overview_mv
    WHERE is_actionable = 1
  `) || 0;

  // Total (visible non-tombstone issues in issue_overview_mv)
  stats.total = execScalar(`SELECT COUNT(*) FROM issue_overview_mv`) || 0;
  stats.active = Math.max(0, stats.total - stats.closed - mvTombstones);

  return stats;
}

/**
 * Get quick wins - actionable issues that unblock the most items
 */
function getQuickWins(limit = 5) {
  let rows;
  try {
    rows = execQuery(`
      SELECT i.*,
             tr.score AS rec_score,
             tr.action AS rec_action,
             tr.reasons AS reasons_json
      FROM issue_overview_mv i
      LEFT JOIN triage_recommendations tr ON i.id = tr.issue_id
      WHERE i.is_actionable = 1
      ORDER BY i.blocks_count DESC, i.triage_score DESC
      LIMIT ?
    `, [limit]);
  } catch {
    rows = execQuery(`
      SELECT * FROM issue_overview_mv
      WHERE is_actionable = 1
      ORDER BY blocks_count DESC, triage_score DESC
      LIMIT ?
    `, [limit]);
  }
  return rows.map(row => {
    const reasons = parseJsonArraySafe(row.reasons_json);
    const fallbackReason = row.blocks_count > 0
      ? `Unblocks ${row.blocks_count} downstream issue${row.blocks_count === 1 ? '' : 's'}`
      : (row.rec_action || 'Ready to work with zero active blockers');
    return {
      ...row,
      triage_action: row.rec_action || '',
      triage_reasons: reasons,
      reason: reasons[0] || fallbackReason,
    };
  });
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
	    WHERE status NOT IN ('closed', 'tombstone')
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
	    WHERE status NOT IN ('closed', 'tombstone')
	    GROUP BY priority
	    ORDER BY priority ASC
	  `);
	}

/**
 * Get top issues by triage score
 */
function getTopPicks(limit = 5) {
  let rows;
  try {
    rows = execQuery(`
      SELECT i.*,
             tr.action AS rec_action,
             tr.reasons AS reasons_json
      FROM issue_overview_mv i
      LEFT JOIN triage_recommendations tr ON i.id = tr.issue_id
      WHERE i.status IN ('open', 'in_progress')
      ORDER BY i.triage_score DESC
      LIMIT ?
    `, [limit]);
  } catch {
    rows = execQuery(`
      SELECT * FROM issue_overview_mv
      WHERE status IN ('open', 'in_progress')
      ORDER BY triage_score DESC
      LIMIT ?
    `, [limit]);
  }
  return rows.map(row => {
    const reasons = parseJsonArraySafe(row.reasons_json);
    return {
      ...row,
      triage_action: row.rec_action || '',
      triage_reasons: reasons,
      reason: reasons[0] || row.rec_action || '',
    };
  });
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

function hasActiveOverviewIssues() {
  return (execScalar(`SELECT COUNT(*) FROM issue_overview_mv WHERE status NOT IN ('closed', 'tombstone')`) || 0) > 0;
}

/**
 * Get top issues by PageRank (active issues first; falls back to closed only if all issues are closed)
 */
function getTopByPageRank(limit = 10) {
  if (hasActiveOverviewIssues()) {
    return execQuery(`
      SELECT * FROM issue_overview_mv
      WHERE pagerank > 0 AND status NOT IN ('closed', 'tombstone')
      ORDER BY pagerank DESC
      LIMIT ?
    `, [limit]);
  }
  return execQuery(`
    SELECT * FROM issue_overview_mv
    WHERE pagerank > 0 AND status <> 'tombstone'
    ORDER BY pagerank DESC
    LIMIT ?
  `, [limit]);
}

/**
 * Get top issues by eigenvector centrality (influencers; active issues unless all issues are closed)
 */
function getTopByEigenvector(limit = 10) {
  const activeOnly = hasActiveOverviewIssues();
  try {
    const results = execQuery(activeOnly ? `
      SELECT * FROM issue_overview_mv
      WHERE eigenvector > 0 AND status NOT IN ('closed', 'tombstone')
      ORDER BY eigenvector DESC
      LIMIT ?
    ` : `
      SELECT * FROM issue_overview_mv
      WHERE eigenvector > 0 AND status <> 'tombstone'
      ORDER BY eigenvector DESC
      LIMIT ?
    `, [limit]);
    if (results.length > 0) return results;
  } catch {
    // Column may not exist in older databases
  }

  if (GRAPH_STATE.ready && typeof GRAPH_STATE.graph?.eigenvectorDefault === 'function') {
    try {
      const ev = GRAPH_STATE.graph.eigenvectorDefault();
      if (ev && ev.length > 0) {
        const ranked = getRankedGraphIssues(ev, 'eigenvector', activeOnly ? Math.max(limit * 4, 50) : limit, value => value > 0);
        if (activeOnly) {
          return ranked.filter(issue => issue && !['closed', 'tombstone'].includes(issue.status)).slice(0, limit);
        }
        return ranked.filter(issue => issue && issue.status !== 'tombstone').slice(0, limit);
      }
    } catch (e) {
      console.warn('[viewer] getTopByEigenvector fallback failed:', e);
    }
  }

  return [];
}

/**
 * Get top issues by triage score
 */
function getTopByTriageScore(limit = 10) {
  return execQuery(`
    SELECT * FROM issue_overview_mv
    WHERE triage_score > 0
      AND LOWER(status) NOT IN ('closed', 'tombstone')
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
      AND LOWER(status) NOT IN ('closed', 'tombstone')
    ORDER BY blocks_count DESC
    LIMIT ?
  `, [limit]);
}

/**
 * Rank exported issue rows by graph scores. Missing or filtered dependency
 * endpoints and closed/tombstone issues do not consume the visible issue limit.
 */
function getRankedGraphIssues(values, field, limit, include = () => true, nodeMap = GRAPH_STATE.nodeMap) {
  if (!GRAPH_STATE.ready || !values?.length || !nodeMap || limit <= 0) return [];

  const ranked = execQuery("SELECT id FROM issue_overview_mv WHERE LOWER(status) NOT IN ('closed', 'tombstone') ORDER BY id")
    .map(({ id }) => ({ id, value: values[nodeMap.get(id)] }))
    .filter(row => row.value !== undefined && include(row.value));
  ranked.sort((a, b) => b.value - a.value);

  return ranked.slice(0, limit).map(row => {
    const issue = getIssue(row.id);
    issue[field] = row.value;
    return issue;
  });
}

/**
 * Get top issues by betweenness centrality (bottlenecks)
 */
function getTopByBetweenness(limit = 10) {
  if (limit <= 0) return [];
  // Try betweenness column if it exists
  try {
    const results = execQuery(`
      SELECT * FROM issue_overview_mv
      WHERE betweenness > 0
        AND LOWER(status) NOT IN ('closed', 'tombstone')
      ORDER BY betweenness DESC
      LIMIT ?
    `, [limit]);
    if (results.length > 0) return results;
  } catch {
    // Column may not exist
  }

  // Fallback: use active WASM graph if available
  const activeGraph = GRAPH_STATE.activeGraph || GRAPH_STATE.graph;
  const activeMap = GRAPH_STATE.activeNodeMap || GRAPH_STATE.nodeMap;
  if (GRAPH_STATE.ready && activeGraph) {
    const count = activeGraph.nodeCount();
    const betweenness = count > 500
      ? activeGraph.betweennessApprox(Math.min(100, count))
      : activeGraph.betweenness();
    if (betweenness && betweenness.length > 0) {
      return getRankedGraphIssues(betweenness, 'betweenness', limit, () => true, activeMap);
    }
  }

  return [];
}

/**
 * Get top issues by critical path depth (keystones)
 */
function getTopByCriticalPath(limit = 10) {
  if (limit <= 0) return [];
  // Try critical_path_depth column if it exists
  try {
    const results = execQuery(`
      SELECT * FROM issue_overview_mv
      WHERE critical_path_depth > 0
        AND LOWER(status) NOT IN ('closed', 'tombstone')
      ORDER BY critical_path_depth DESC
      LIMIT ?
    `, [limit]);
    if (results.length > 0) return results;
  } catch {
    // Column may not exist
  }

  // Fallback: use active WASM graph if available
  const activeGraph = GRAPH_STATE.activeGraph || GRAPH_STATE.graph;
  const activeMap = GRAPH_STATE.activeNodeMap || GRAPH_STATE.nodeMap;
  if (GRAPH_STATE.ready && activeGraph && !activeGraph.hasCycles()) {
    const heights = activeGraph.criticalPathHeights();
    if (heights && heights.length > 0) {
      return getRankedGraphIssues(heights, 'critical_path_depth', limit, value => value > 0, activeMap);
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
    if (!hitsResult || !hitsResult.hubs) return [];

    return getRankedGraphIssues(hitsResult.hubs, 'hits_hub', limit);
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
    if (!hitsResult || !hitsResult.authorities) return [];

    return getRankedGraphIssues(hitsResult.authorities, 'hits_auth', limit);
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
  const activeGraph = GRAPH_STATE.activeGraph || GRAPH_STATE.graph;
  const activeMap = GRAPH_STATE.activeNodeMap || GRAPH_STATE.nodeMap;
  if (!GRAPH_STATE.ready || !activeGraph) return [];

  try {
    const kcoreValues = activeGraph.kcore();
    if (!kcoreValues || kcoreValues.length === 0) return [];

    return getRankedGraphIssues(kcoreValues, 'kcore', limit, () => true, activeMap);
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
  const activeGraph = GRAPH_STATE.activeGraph || GRAPH_STATE.graph;
  if (!GRAPH_STATE.ready || !activeGraph) return [];

  try {
    const artPoints = activeGraph.articulationPoints();
    if (!artPoints || artPoints.length === 0) return [];

    const resolvedIds = getResolvedIssueIDs();
    return Array.from(artPoints).map(idx => {
      const id = activeGraph.nodeId(idx);
      if (!id || resolvedIds.has(id)) return null;
      const issue = getIssue(id);
      if (!issue) return null;
      const st = String(issue.status || '').trim().toLowerCase();
      if (st === 'closed' || st === 'tombstone') return null;
      issue.is_articulation = true;
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
  const activeGraph = GRAPH_STATE.activeGraph || GRAPH_STATE.graph;
  const activeMap = GRAPH_STATE.activeNodeMap || GRAPH_STATE.nodeMap;
  if (!GRAPH_STATE.ready || !activeGraph || activeGraph.hasCycles()) return [];

  try {
    const slackValues = activeGraph.slack();
    if (!slackValues || slackValues.length === 0) return [];

    if (!showZeroSlack) {
      const issues = getRankedGraphIssues(slackValues, 'slack', limit, () => true, activeMap);
      for (const issue of issues) {
        issue.on_critical_path = Math.abs(issue.slack || 0) < 1e-9;
      }
      return issues;
    }

    const activeIds = new Set(
      execQuery(`SELECT id FROM issue_overview_mv WHERE LOWER(status) NOT IN ('closed', 'tombstone')`).map(r => r.id)
    );
    const issues = getRankedGraphIssues(
      slackValues,
      'slack',
      Math.max(limit * 2, activeIds.size || limit),
      value => Math.abs(value) < 1e-9,
      activeMap
    ).filter(issue => issue && activeIds.has(issue.id)).slice(0, limit);
    for (const issue of issues) {
      issue.on_critical_path = true;
    }
    return issues;
  } catch (e) {
    console.warn('[viewer] getIssuesBySlack failed:', e);
    return [];
  }
}

let LATEST_TRIAGE_DATA = null;

/**
 * Compute deterministic Tarjan SCCs and real-edge elementary directed cycles
 * over active blocking edges when WASM graph engine is unavailable.
 */
function computeFallbackActiveCycles(activeIds, directedEdges, maxCycles = 100) {
  const sortedIds = [...new Set((activeIds || []).filter(Boolean))].sort();
  const n = sortedIds.length;
  if (n === 0 || !Array.isArray(directedEdges) || directedEdges.length === 0) {
    return { sccComponents: [], cycles: [], truncated: false };
  }
  const idToIdx = new Map(sortedIds.map((id, i) => [id, i]));
  const adjSets = Array.from({ length: n }, () => new Set());
  const selfLoopSet = new Set();
  for (const [fromId, toId] of directedEdges) {
    const u = idToIdx.get(fromId);
    const v = idToIdx.get(toId);
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
    truncated,
  };
}

/**
 * Get cycle information from active graph (closed/tombstone historical cycles excluded)
 */
function getCycleInfo(triageDataOverride = LATEST_TRIAGE_DATA) {
  const activeGraph = GRAPH_STATE.activeGraph || GRAPH_STATE.graph;
  if (!GRAPH_STATE.ready || !activeGraph) {
    let activeIds = [];
    let mvCycleIds = [];
    let activeBlockingEdges = [];
    try {
      if (DB_STATE.db) {
        const hasInCycleCol = getOverviewColumnSet().has('in_cycle');
        const rows = execQuery(`
          SELECT id${hasInCycleCol ? ', COALESCE(in_cycle, 0) AS in_cycle' : ''}
          FROM issue_overview_mv
          WHERE LOWER(status) NOT IN ('closed', 'tombstone')
          ORDER BY id
        `);
        activeIds = rows.map(r => r.id).filter(Boolean);
        if (hasInCycleCol) {
          mvCycleIds = rows.filter(r => Number(r.in_cycle) === 1).map(r => r.id).filter(Boolean);
        }
        const activeSet = new Set(activeIds);
        const depRows = execQuery(`
          SELECT issue_id, depends_on_id
          FROM dependencies
          WHERE COALESCE(type, 'blocks') IN ('', 'blocks', 'conditional-blocks', 'waits-for')
        `);
        activeBlockingEdges = depRows
          .filter(d => activeSet.has(d.issue_id) && activeSet.has(d.depends_on_id))
          .map(d => [d.issue_id, d.depends_on_id]);
      }
    } catch {
      activeIds = [];
      mvCycleIds = [];
      activeBlockingEdges = [];
    }
    const computed = computeFallbackActiveCycles(activeIds, activeBlockingEdges, 100);
    const graphHealth = triageDataOverride?.project_health?.graph
      ?? triageDataOverride?.triage?.project_health?.graph
      ?? null;
    const rawHealthCount = Number(graphHealth?.cycle_count) || 0;
    // Respect explicit MV acyclic override when caller cleared in_cycle=0 and health reports acyclic
    const explicitMvAndHealthAcyclic = graphHealth !== null
      && !Boolean(graphHealth?.has_cycles)
      && rawHealthCount === 0
      && mvCycleIds.length === 0;
    const sccComponents = explicitMvAndHealthAcyclic
      ? []
      : (computed.sccComponents.length > 0
        ? computed.sccComponents
        : mvCycleIds.map(id => [id]));
    const hasCycles = !explicitMvAndHealthAcyclic && (
      computed.cycles.length > 0 ||
      sccComponents.length > 0 ||
      Boolean(graphHealth?.has_cycles) ||
      rawHealthCount > 0 ||
      mvCycleIds.length > 0
    );
    const largestSccSize = sccComponents.reduce((max, comp) => Math.max(max, comp.length), 0);
    const cycleCount = hasCycles
      ? Math.max(rawHealthCount, computed.cycles.length, sccComponents.length)
      : 0;
    return {
      hasCycles,
      cycleCount,
      truncated: Boolean(computed.truncated),
      sccCount: sccComponents.length,
      nonTrivialSccCount: sccComponents.length || (hasCycles ? 1 : 0),
      largestSccSize,
      sccComponents,
      suggestions: null,
    };
  }

  const hasCycles = activeGraph.hasCycles();
  const scc = hasCycles && typeof activeGraph.tarjanScc === 'function'
    ? activeGraph.tarjanScc()
    : null;
  const rawComponents = Array.isArray(scc?.components) ? scc.components : [];
  const nonTrivialComponents = rawComponents.filter(comp => {
    if (!Array.isArray(comp) || comp.length === 0) return false;
    if (comp.length > 1) return true;
    const u = comp[0];
    const succ = typeof activeGraph.successors === 'function' ? activeGraph.successors(u) : [];
    return Array.isArray(succ) && succ.includes(u);
  });
  const largestSccSize = nonTrivialComponents.reduce((max, comp) => Math.max(max, comp.length), 0);
  const sccComponents = nonTrivialComponents.map(comp =>
    comp.map(idx => activeGraph.nodeId(idx)).filter(Boolean)
  );

  const enumInfo = hasCycles && typeof activeGraph.enumerateCycles === 'function'
    ? activeGraph.enumerateCycles(100)
    : null;
  const suggestions = hasCycles ? getCycleBreakSuggestions(5) : null;
  if (suggestions && !suggestions.suggestions && Array.isArray(suggestions.top_suggestions)) {
    suggestions.suggestions = suggestions.top_suggestions;
  }
  const rawCycleCount = suggestions?.total_cycles
    ?? suggestions?.cycles?.length
    ?? enumInfo?.count
    ?? 0;
  const cycleCount = hasCycles
    ? Math.max(Number(rawCycleCount) || 0, nonTrivialComponents.length)
    : 0;
  const truncated = Boolean(suggestions?.truncated || enumInfo?.truncated);

  return {
    hasCycles,
    cycleCount,
    truncated,
    sccCount: rawComponents.length,
    nonTrivialSccCount: Math.max(Number(scc?.cycle_count) || 0, nonTrivialComponents.length),
    largestSccSize,
    sccComponents,
    suggestions,
  };
}

/**
 * Get the full critical path sequence from root to sink on the active DAG.
 * Returns array of issue IDs representing the longest dependency chain.
 */
function getCriticalPathSequence() {
  const graph = GRAPH_STATE.activeGraph || GRAPH_STATE.graph;
  if (!GRAPH_STATE.ready || !graph || graph.hasCycles()) return null;

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
  const blockedBy = execQuery(`
    SELECT i.* FROM issue_overview_mv i
    JOIN dependencies d ON i.id = d.depends_on_id
    WHERE d.issue_id = ? AND COALESCE(d.type, 'blocks') IN ${BLOCKING_DEPENDENCY_TYPES_SQL}
    ORDER BY i.id
  `, [id]);

  const blocks = execQuery(`
    SELECT i.* FROM issue_overview_mv i
    JOIN dependencies d ON i.id = d.issue_id
    WHERE d.depends_on_id = ? AND COALESCE(d.type, 'blocks') IN ${BLOCKING_DEPENDENCY_TYPES_SQL}
    ORDER BY i.id
  `, [id]);

  return { blocks, blockedBy };
}

/**
 * Get categorized dependencies (Blocking / Blocked By, Parent / Children, Related / Discovered From)
 * for the Issue Detail Modal, preserving non-blocking relationship edges without affecting blocking APIs.
 */
function getIssueDependencyGroups(id) {
  const groups = {
    blocks: [],
    blockedBy: [],
    parents: [],
    children: [],
    related: [],
  };
  if (!id) return groups;

  try {
    const outgoing = execQuery(`
      SELECT d.depends_on_id AS dep_id,
             COALESCE(d.type, 'blocks') AS dep_type,
             i.id AS mv_id,
             i.title,
             i.status,
             i.priority,
             i.issue_type,
             raw_i.title AS raw_title,
             raw_i.status AS raw_status
      FROM dependencies d
      LEFT JOIN issue_overview_mv i ON i.id = d.depends_on_id
      LEFT JOIN issues raw_i ON raw_i.id = d.depends_on_id
      WHERE d.issue_id = ?
      ORDER BY d.depends_on_id
    `, [id]);

    const incoming = execQuery(`
      SELECT d.issue_id AS dep_id,
             COALESCE(d.type, 'blocks') AS dep_type,
             i.id AS mv_id,
             i.title,
             i.status,
             i.priority,
             i.issue_type,
             raw_i.title AS raw_title,
             raw_i.status AS raw_status
      FROM dependencies d
      LEFT JOIN issue_overview_mv i ON i.id = d.issue_id
      LEFT JOIN issues raw_i ON raw_i.id = d.issue_id
      WHERE d.depends_on_id = ?
      ORDER BY d.issue_id
    `, [id]);

    const blockingTypes = new Set(['', 'blocks', 'conditional-blocks', 'waits-for']);
    const seenRelated = new Set();

    const toEntry = (row, direction) => {
      const hasMvRow = Boolean(row.mv_id) || Boolean(row.title || row.status);
      const hasRawRow = Boolean(row.raw_status);
      let status = 'missing';
      let title = `${row.dep_id} (missing)`;
      if (hasMvRow) {
        status = row.status || 'open';
        title = row.title || row.dep_id;
      } else if (hasRawRow) {
        status = row.raw_status;
        title = row.raw_title ? `${row.raw_title} (${row.raw_status})` : `${row.dep_id} (${row.raw_status})`;
      }
      return {
        id: row.dep_id,
        title,
        status,
        priority: row.priority ?? 2,
        issue_type: row.issue_type || 'task',
        dep_type: row.dep_type || 'blocks',
        direction,
        available: Boolean(row.mv_id),
      };
    };

    for (const row of outgoing) {
      const depType = row.dep_type ?? '';
      if (blockingTypes.has(depType)) {
        groups.blockedBy.push(toEntry(row, 'upstream'));
      } else if (depType === 'parent-child') {
        groups.parents.push(toEntry(row, 'parent'));
      } else {
        const normalizedType = depType || 'related';
        const key = `${row.dep_id}:${normalizedType}`;
        if (!seenRelated.has(key)) {
          seenRelated.add(key);
          groups.related.push({ ...toEntry(row, 'outgoing'), dep_type: normalizedType });
        }
      }
    }

    for (const row of incoming) {
      const depType = row.dep_type ?? '';
      if (blockingTypes.has(depType)) {
        groups.blocks.push(toEntry(row, 'downstream'));
      } else if (depType === 'parent-child') {
        groups.children.push(toEntry(row, 'child'));
      } else {
        const normalizedType = depType || 'related';
        const key = `${row.dep_id}:${normalizedType}`;
        if (!seenRelated.has(key)) {
          seenRelated.add(key);
          groups.related.push({ ...toEntry(row, 'incoming'), dep_type: normalizedType });
        }
      }
    }
  } catch (err) {
    console.warn('[viewer] getIssueDependencyGroups failed:', err);
  }

  return groups;
}

// ============================================================================
// URL State Sync - Shareable filtered views
// ============================================================================

const VALID_SEARCH_MODES = new Set(['text', 'hybrid']);
const VALID_SEARCH_PRESETS = new Set(['default', 'bug-hunting', 'sprint-planning', 'impact-first', 'text-only']);

/**
 * Serialize filters to URL search params
 */
function filtersToURL(filters = {}, sort = 'priority', searchQuery = '', searchMode = 'text', searchPreset = 'default') {
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

  if (filters.criticalPath === true || filters.criticalPath === 'true') {
    params.set('critical', 'true');
  }

  if (searchQuery) {
    params.set('q', searchQuery);
  }

  if (searchMode && VALID_SEARCH_MODES.has(searchMode) && searchMode !== 'text') {
    params.set('mode', searchMode);
  }

  if (searchPreset && VALID_SEARCH_PRESETS.has(searchPreset) && searchPreset !== 'default') {
    params.set('preset', searchPreset);
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
  if (queryIndex === -1) {
    return {
      filters: {},
      sort: 'priority',
      searchQuery: '',
      searchMode: 'text',
      searchPreset: 'default',
    };
  }

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
  if (blockedParam === 'true' || blockedParam === '1') {
    filters.hasBlockers = true;
  } else if (blockedParam === 'false' || blockedParam === '0') {
    filters.hasBlockers = false;
  }

  const blockingParam = params.get('blocking');
  if (blockingParam === 'true' || blockingParam === '1') {
    filters.isBlocking = true;
  }

  const criticalParam = params.get('critical');
  if (criticalParam === 'true' || criticalParam === '1') {
    filters.criticalPath = true;
  }

  const rawMode = params.get('mode') || 'text';
  const searchMode = VALID_SEARCH_MODES.has(rawMode) ? rawMode : 'text';
  const rawPreset = params.get('preset') || 'default';
  const searchPreset = VALID_SEARCH_PRESETS.has(rawPreset) ? rawPreset : 'default';

  return {
    filters,
    sort: params.get('sort') || 'priority',
    searchQuery: params.get('q') || '',
    searchMode,
    searchPreset,
  };
}

/**
 * Update URL with current filter state (without page reload)
 */
function syncFiltersToURL(view, filters, sort, searchQuery, searchMode = 'text', searchPreset = 'default') {
  if (window.location.hash.startsWith('#/issue/')) return;
  const paramString = filtersToURL(filters, sort, searchQuery, searchMode, searchPreset);
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
function navigateToIssues(filters = {}, sort = 'priority', search = '', searchMode = 'text', searchPreset = 'default') {
  const params = filtersToURL(filters, sort, search, searchMode, searchPreset);
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

    const diffMs = getReferenceNowMs() - date.getTime();

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

const DEFAULT_TRIAGE_WEIGHTS = {
  pagerank: 0.22,
  betweenness: 0.20,
  blocker_ratio: 0.13,
  priority_boost: 0.10,
  time_to_impact: 0.10,
  urgency: 0.10,
  risk: 0.10,
  staleness: 0.05,
};

function resolveEffectiveTriageWeights(triageData) {
  const base = { ...DEFAULT_TRIAGE_WEIGHTS };
  if (!triageData || typeof triageData !== 'object') return base;
  const root = triageData.triage && typeof triageData.triage === 'object' ? triageData.triage : triageData;
  const fb = triageData.feedback || root.feedback || null;
  const candidate =
    fb?.effective_weights ||
    fb?.adjusted_weights ||
    fb?.weights ||
    triageData.effective_weights ||
    root.effective_weights ||
    triageData.weights ||
    root.weights ||
    null;

  const pickNum = (obj, ...keys) => {
    if (!obj || typeof obj !== 'object') return undefined;
    for (const k of keys) {
      if (typeof obj[k] === 'number' && Number.isFinite(obj[k]) && obj[k] >= 0) {
        return obj[k];
      }
    }
    return undefined;
  };

  if (candidate && typeof candidate === 'object') {
    const mapped = {
      pagerank: pickNum(candidate, 'pagerank', 'PageRank'),
      betweenness: pickNum(candidate, 'betweenness', 'Betweenness'),
      blocker_ratio: pickNum(candidate, 'blocker_ratio', 'BlockerRatio', 'blockers'),
      priority_boost: pickNum(candidate, 'priority_boost', 'PriorityBoost', 'priority'),
      time_to_impact: pickNum(candidate, 'time_to_impact', 'TimeToImpact', 'time'),
      urgency: pickNum(candidate, 'urgency', 'Urgency'),
      risk: pickNum(candidate, 'risk', 'Risk'),
      staleness: pickNum(candidate, 'staleness', 'Staleness'),
    };
    if (Object.values(mapped).some(v => v !== undefined)) {
      for (const [k, v] of Object.entries(mapped)) {
        if (v !== undefined) base[k] = v;
      }
      return base;
    }
  }

  const byFactor = fb?.by_factor;
  if (byFactor && typeof byFactor === 'object') {
    let adjusted = false;
    for (const k of Object.keys(base)) {
      const adj = byFactor[k]?.weight_adjustment ?? byFactor[k]?.multiplier;
      if (typeof adj === 'number' && Number.isFinite(adj) && adj > 0) {
        base[k] = base[k] * adj;
        adjusted = true;
      }
    }
    if (adjusted) {
      const sum = Object.values(base).reduce((a, b) => a + b, 0);
      if (sum > 0) {
        for (const k of Object.keys(base)) {
          base[k] = base[k] / sum;
        }
      }
    }
  }

  return base;
}

/**
 * Calculate score breakdown as percentages for visualization
 * Returns array of { name, value, percent, color, weight } sorted by contribution
 */
function getScoreBreakdownBars(breakdown, weightsOverride = null) {
  if (!breakdown) return [];

  const effWeights = weightsOverride || DEFAULT_TRIAGE_WEIGHTS;
  const components = [
    { name: 'PageRank', key: 'pagerank', color: '#3b82f6', colorClass: 'sb-c-pagerank', weight: effWeights.pagerank ?? 0.22 },
    { name: 'Betweenness', key: 'betweenness', color: '#f97316', colorClass: 'sb-c-betweenness', weight: effWeights.betweenness ?? 0.20 },
    { name: 'Blocker Ratio', key: 'blocker_ratio', color: '#ef4444', colorClass: 'sb-c-blocker_ratio', weight: effWeights.blocker_ratio ?? 0.13 },
    { name: 'Priority', key: 'priority_boost', color: '#8b5cf6', colorClass: 'sb-c-priority_boost', weight: effWeights.priority_boost ?? 0.10 },
    { name: 'Time Impact', key: 'time_to_impact', color: '#06b6d4', colorClass: 'sb-c-time_to_impact', weight: effWeights.time_to_impact ?? 0.10 },
    { name: 'Urgency', key: 'urgency', color: '#ec4899', colorClass: 'sb-c-urgency', weight: effWeights.urgency ?? 0.10 },
    { name: 'Risk', key: 'risk', color: '#f59e0b', colorClass: 'sb-c-risk', weight: effWeights.risk ?? 0.10 },
    { name: 'Staleness', key: 'staleness', color: '#6b7280', colorClass: 'sb-c-staleness', weight: effWeights.staleness ?? 0.05 },
  ];

  const total = components.reduce((sum, c) => sum + (breakdown[c.key] || 0), 0);

  return components
    .map(c => {
      const value = breakdown[c.key] || 0;
      const percent = total > 0 ? (value / total * 100) : 0;
      const normPct = breakdown[c.key + '_norm'] !== undefined
        ? (breakdown[c.key + '_norm'] || 0) * 100
        : percent;
      const clamped = Math.max(0, Math.min(100, normPct));
      const bucket = value > 0 ? Math.max(5, Math.round(clamped / 5) * 5) : 0;
      return {
        name: c.name,
        key: c.key,
        value,
        percent,
        color: c.color,
        colorClass: c.colorClass,
        weight: c.weight,
        // Normalized value for bar width (0-100)
        normalized: clamped,
        widthClass: `sb-w-${bucket}`,
      };
    })
    .filter(c => c.value > 0)
    .sort((a, b) => b.value - a.value);
}

function escapeRawHtmlForDisplay(value) {
  return String(value ?? '').replace(/[&<>"']/g, ch => ({
    '&': '&amp;',
    '<': '&lt;',
    '>': '&gt;',
    '"': '&quot;',
    "'": '&#39;'
  }[ch]));
}

function stripInlineEventAttributes(rawHtml) {
  const input = String(rawHtml ?? '');
  if (input.indexOf('<') === -1) return input;
  // Strip inline on*= event-handler attributes on raw HTML start tags before
  // DOMPurify invokes DOMParser so Chromium does not emit parser-time CSP
  // inline-handler errors prior to DOMPurify's authoritative sanitization.
  return input.replace(/<[a-zA-Z](?:"[^"]*"|'[^']*'|[^>"'])*>/g, tag => {
    let cur = tag;
    let prev;
    do {
      prev = cur;
      cur = cur.replace(/(?:[\s/]+)on[a-zA-Z0-9_-]+\s*=\s*(?:"[^"]*"|'[^']*'|[^\s>"']*)/gi, ' ');
    } while (cur !== prev);
    return cur;
  });
}

let SAFE_MARKED_INSTANCE = null;

function getSafeMarkedInstance() {
  if (SAFE_MARKED_INSTANCE) return SAFE_MARKED_INSTANCE;
  if (typeof marked !== 'undefined' && typeof marked.Marked === 'function') {
    SAFE_MARKED_INSTANCE = new marked.Marked({
      renderer: {
        html(token) {
          const raw = typeof token === 'string' ? token : (token?.raw ?? token?.text ?? '');
          return stripInlineEventAttributes(raw);
        }
      }
    });
  }
  return SAFE_MARKED_INSTANCE;
}

/**
 * Format JSON with syntax highlighting for display
 */
function formatJsonWithHighlight(obj) {
  if (!obj) return '';
  try {
    const json = JSON.stringify(obj, null, 2)
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;');
    // Add syntax highlighting classes
    return json
      .replace(/"([^"\\]*(?:\\.[^"\\]*)*)":/g, '<span class="text-purple-600 dark:text-purple-400">"$1"</span>:')
      .replace(/: "([^"\\]*(?:\\.[^"\\]*)*)"/g, ': <span class="text-green-600 dark:text-green-400">"$1"</span>')
      .replace(/: (-?\d+\.?\d*)/g, ': <span class="text-blue-600 dark:text-blue-400">$1</span>')
      .replace(/: (true|false)/g, ': <span class="text-amber-600 dark:text-amber-400">$1</span>')
      .replace(/: (null)/g, ': <span class="text-gray-500 dark:text-gray-400">$1</span>');
  } catch {
    return escapeRawHtmlForDisplay(obj);
  }
}

/**
 * Render markdown safely
 */
function renderMarkdown(text) {
  if (!text) return '';
  try {
    const parser = getSafeMarkedInstance();
    const html = parser ? parser.parse(String(text)) : stripInlineEventAttributes(text);
    return DOMPurify.sanitize(html);
  } catch {
    return DOMPurify.sanitize(stripInlineEventAttributes(text));
  }
}

/**
 * Render markdown as inline HTML (no block elements) for excerpts
 * Converts markdown to HTML but wraps in a span to work with line-clamp
 */
function renderMarkdownInline(text) {
  if (!text) return '';
  try {
    const parser = getSafeMarkedInstance();
    // Use marked's parseInline to avoid block elements like <p>
    const html = parser ? parser.parseInline(String(text)) : stripInlineEventAttributes(text);
    return DOMPurify.sanitize(html);
  } catch {
    return DOMPurify.sanitize(stripInlineEventAttributes(text));
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
      criticalPath: false, // Filter to critical path issues
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

    // Selected issue & list cursor
    selectedIssue: null,
    copyLinkMessage: '',
    showDepGraph: false,
    issueNavList: [], // List of issue IDs for j/k navigation
    focusedIssueIndex: -1, // Highlighted row cursor in #/issues list view
    showKeyboardHelp: false, // Keyboard shortcuts help modal

    // Graph search state (top-center search overlay in #/graph)
    graphSearchQuery: '',
    graphSearchResult: null,
    graphSearchOpen: false,

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
    graphStageTimer: null,

    // Heatmap & metrics mode
    currentPreset: 'force',
    graphHeatmapActive: false,
    graphSizeMetric: 'pagerank', // pagerank | betweenness | critical | indegree
    timeTravelAvailable: false,
    timeTravelActive: false,

    // Cycle navigator state (synced between Insights and Graph views)
    cycleNavigatorActive: false,
    currentCycleIdx: 0,
    totalCycles: 0,
    currentCyclePath: '',

    // Critical path highlighting & [1/N] kCriticalPaths stepper
    showCriticalPath: false,
    criticalPathData: null, // { path: [issueIds], length: number, animating: boolean }
    criticalPathAnimationStep: -1,
    criticalPathNavActive: false,
    currentCriticalPathIdx: 0,
    totalCriticalPaths: 0,
    currentCriticalPathLabel: '',
    topCriticalPaths: [],

    // Insights view data
    topByEigenvector: [],
    topByBetweenness: [],
    topByCriticalPath: [],
    topByHITSHub: [],
    topByHITSAuth: [],
    topByKCore: [],
    articulationPoints: [],
    criticalPathSlack: [], // Issues with 0 slack (on critical path)
    flexibleSlackIssues: [], // Issues with positive schedule slack
    cycleInfo: null,
    coverageSet: null,
    topImpactIssues: [],
    includeBlockedWhatIf: false,
    parallelCutData: null,
    parallelCutIssues: [],
    bridgeEdges: [],

    // Transitive dependency cone isolation in Graph view
    dependencyConeActive: false,
    dependencyConeFocusId: null,
    dependencyConeCount: 0,

    // Full triage data from triage.json (robot mode output)
    triageData: null,
    triageRecView: 'top', // 'top' | 'track' | 'label'
    copiedCommandText: '',
    showTriageJson: false, // Modal for raw JSON view

    syncModalOpenBodyClass() {
      const hasModal = !!(this.selectedIssue || (this.view === 'graph' && this.graphDetailNode));
      if (hasModal) {
        if (!document.body.classList.contains('modal-open')) {
          document.body.style.setProperty('--scroll-y', `${window.scrollY}px`);
          document.body.classList.add('modal-open');
        }
      } else if (document.body.classList.contains('modal-open')) {
        const scrollY = document.body.style.getPropertyValue('--scroll-y');
        document.body.classList.remove('modal-open');
        if (scrollY) {
          window.scrollTo(0, parseInt(scrollY || '0', 10));
        }
      }
    },

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
      const updateBodyScrollLock = () => this.syncModalOpenBodyClass();

      // Watch for modal state changes using Alpine's $watch
      this.$watch('selectedIssue', updateBodyScrollLock);
      this.$watch('graphDetailNode', updateBodyScrollLock);

      // Scroll to top on view change, hide stray graph tooltips, and guard direct view transitions
      this.$watch('view', (newView, oldView) => {
        if (newView !== 'graph') {
          if (this.graphDetailNode) {
            this.graphDetailNode = null;
          }
          if (oldView === 'graph') {
            this.forceGraphModule?.hideTooltip?.();
          }
          this.syncModalOpenBodyClass();
        }
        if (newView === 'graph' && !this.forceGraphReady && !this.forceGraphLoading && DB_STATE.db) {
          this.$nextTick(() => this.initForceGraphView());
        }
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
          window.BVCharts?.applyTheme?.(this.darkMode);
          this.forceGraphModule?.setGraphTheme?.(this.darkMode);
          // Re-render Mermaid graphs if visible
          if (this.showDepGraph && this.selectedIssue) {
            this.renderDepGraph();
          }
        }
      });

      // Close coarse-pointer / click-opened .metric-tooltip cards when clicking outside
      document.addEventListener('click', (e) => {
        if (!e.target?.closest?.('.metric-tooltip-trigger')) {
          document.querySelectorAll('.metric-tooltip.tooltip-visible').forEach(el => el.classList.remove('tooltip-visible'));
          document.querySelectorAll('.metric-tooltip-trigger.tooltip-open').forEach(el => el.classList.remove('tooltip-open'));
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
        // Ensure required properties and unique key even under sub-ms bursts
        const candidateId = toast.id ? String(toast.id) : nextToastId();
        const uniqueId = this.toasts.some(t => t.id === candidateId) ? nextToastId() : candidateId;
        const validToast = {
          id: uniqueId,
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
        // Skip if typing in input or select fields
        const isInput = ['INPUT', 'TEXTAREA', 'SELECT'].includes(e.target?.tagName) || Boolean(e.target?.isContentEditable);

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

        // 'c' toggles critical path highlighting (scoped away from graph view, where graph.js handles 'c')
        if (e.key === 'c' && !isInput) {
          if (this.view === 'graph') return;
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

        // Issues list keyboard navigation (j/k cursor + Enter/o open when no modal or input is active)
        if (!isInput && this.view === 'issues' && !this.selectedIssue && !this.showKeyboardHelp && !this.showDiagnostics) {
          if (e.key === 'j') {
            if (this.issues.length > 0) {
              e.preventDefault();
              this.focusedIssueIndex = this.focusedIssueIndex < 0
                ? 0
                : Math.min(this.issues.length - 1, this.focusedIssueIndex + 1);
              this.$nextTick(() => {
                document.querySelector(`[data-issue-idx="${this.focusedIssueIndex}"]`)?.scrollIntoView?.({ block: 'nearest' });
              });
            }
            return;
          }
          if (e.key === 'k') {
            if (this.issues.length > 0) {
              e.preventDefault();
              this.focusedIssueIndex = this.focusedIssueIndex <= 0 ? 0 : this.focusedIssueIndex - 1;
              this.$nextTick(() => {
                document.querySelector(`[data-issue-idx="${this.focusedIssueIndex}"]`)?.scrollIntoView?.({ block: 'nearest' });
              });
            }
            return;
          }
          if (e.key === 'o') {
            if (this.issues.length > 0) {
              e.preventDefault();
              const idx = this.focusedIssueIndex >= 0 && this.focusedIssueIndex < this.issues.length ? this.focusedIssueIndex : 0;
              this.focusedIssueIndex = idx;
              this.selectIssue(this.issues[idx].id);
            }
            return;
          }
          if (e.key === 'Enter') {
            const tag = e.target?.tagName;
            const role = e.target?.getAttribute?.('role');
            if (!['BUTTON', 'A', 'SUMMARY'].includes(tag) && role !== 'button' && this.issues.length > 0 && this.focusedIssueIndex >= 0 && this.focusedIssueIndex < this.issues.length) {
              e.preventDefault();
              this.selectIssue(this.issues[this.focusedIssueIndex].id);
              return;
            }
          }
        }
      });

      // Focus #/graph node search input on Ctrl+F / Cmd+F from graph.js
      window.addEventListener('bv-graph:searchRequest', () => {
        if (this.view !== 'graph') return;
        this.graphSearchOpen = true;
        this.$nextTick(() => {
          const input = document.getElementById('graph-search-input');
          if (input) {
            input.focus();
            input.select?.();
          }
        });
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

      // Sync graph cycle navigator, view mode, and time-travel state
      document.addEventListener('bv-graph:cycleHighlightChange', (e) => {
        this.cycleNavigatorActive = true;
        this.currentCycleIdx = e.detail?.currentIndex ?? 0;
        this.totalCycles = e.detail?.cycleCount ?? (this.cycleInfo?.cycleCount || 0);
        this.currentCyclePath = e.detail?.cyclePath || '';
      });
      document.addEventListener('bv-graph:cycleNavigatorReset', () => {
        this.cycleNavigatorActive = false;
        this.currentCycleIdx = 0;
        this.currentCyclePath = '';
      });
      document.addEventListener('bv-graph:criticalPathChange', (e) => {
        this.criticalPathNavActive = Boolean(e.detail?.active ?? true);
        this.currentCriticalPathIdx = e.detail?.currentIndex ?? 0;
        this.totalCriticalPaths = e.detail?.pathCount ?? (this.topCriticalPaths?.length || 0);
        this.currentCriticalPathLabel = e.detail?.pathLabel || (Array.isArray(e.detail?.path) ? e.detail.path.join(' → ') : '');
      });
      document.addEventListener('bv-graph:criticalPathReset', () => {
        this.criticalPathNavActive = false;
        this.currentCriticalPathIdx = 0;
        this.currentCriticalPathLabel = '';
      });
      document.addEventListener('bv-graph:timeTravelReady', (e) => {
        this.timeTravelAvailable = (e.detail?.commits || 0) > 0;
      });
      document.addEventListener('bv-graph:timeTravelStart', () => {
        this.timeTravelActive = true;
      });
      document.addEventListener('bv-graph:timeTravelStop', () => {
        this.timeTravelActive = false;
      });
      document.addEventListener('bv-graph:viewModeChange', (e) => {
        if (e.detail?.preset) {
          this.currentPreset = e.detail.preset;
        }
      });

      // Dashboard chart click-to-filter integration
      const applyChartDrilldown = (filterOverrides) => {
        this.selectedIssue = null;
        this.graphDetailNode = null;
        this.showCriticalPath = false;
        this.criticalPathData = null;
        this.criticalPathAnimationStep = -1;
        this.searchQuery = '';
        this.filters = {
          status: [],
          type: [],
          priority: [],
          labels: [],
          assignee: '',
          hasBlockers: null,
          isBlocking: null,
          criticalPath: false,
          ...filterOverrides,
        };
        this.page = 1;
        navigateToIssues(this.filters, this.sort, '', this.searchMode, this.searchPreset);
      };
      document.addEventListener('bv-chart:priorityClick', (e) => {
        const priority = e.detail?.priority;
        if (priority === undefined || priority === null) return;
        applyChartDrilldown({ priority: [Number(priority)] });
      });
      document.addEventListener('bv-chart:typeClick', (e) => {
        const type = e.detail?.type;
        if (!type) return;
        applyChartDrilldown({ type: [String(type)] });
      });
      document.addEventListener('bv-chart:labelClick', (e) => {
        const label = e.detail?.label;
        if (!label) return;
        applyChartDrilldown({ labels: [String(label)] });
      });
      document.addEventListener('bv-chart:blockerClick', (e) => {
        const issueId = e.detail?.id || e.detail?.issueId;
        if (issueId) {
          this.showIssue(String(issueId));
          return;
        }
        applyChartDrilldown({ isBlocking: true });
      });
      document.addEventListener('bv-chart:heatmapClick', (e) => {
        const labels = [...new Set([e.detail?.fromLabel, e.detail?.toLabel].filter(Boolean))];
        if (labels.length === 0) return;
        applyChartDrilldown({ labels });
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
        window.BV_META = this.meta;
        getReferenceNowMs();
        this.stats = getStats();
        DIAGNOSTICS.issueCount = this.stats.total || 0;
        if (typeof window.initHybridWasmScorer === 'function') {
          window.initHybridWasmScorer(DIAGNOSTICS.issueCount, this.meta)
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
        this.topByEigenvector = getTopByEigenvector(10);
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
          this.coverageSet = getCoverageSet(10);
          this.topByBetweenness = getTopByBetweenness(10);
          this.topByCriticalPath = getTopByCriticalPath(10);
          if (!this.topByEigenvector.length) {
            this.topByEigenvector = getTopByEigenvector(10);
          }
          this.cycleInfo = getCycleInfo();
          this.totalCycles = this.cycleInfo?.cycleCount || 0;
          this.topImpactIssues = topWhatIf(10);
          // Additional TUI-style metrics
          this.topByHITSHub = getTopByHITSHub(10);
          this.topByHITSAuth = getTopByHITSAuth(10);
          this.topByKCore = getTopByKCore(10);
          this.articulationPoints = getArticulationPoints();
          this.criticalPathSlack = getIssuesBySlack(10, true); // Zero slack = critical path
          this.flexibleSlackIssues = getIssuesBySlack(25, false)
            .filter(i => (i.slack || 0) > 0 && i.status !== 'closed' && i.status !== 'tombstone')
            .slice(0, 10);
          this.parallelCutData = getParallelCutData(10);
          this.parallelCutIssues = this.parallelCutData.items || [];
          this.bridgeEdges = getBridgeDependencies(15);
          this.topCriticalPaths = getTopCriticalPaths(5);
          this.totalCriticalPaths = this.topCriticalPaths.length;
        } else {
          this.topByBetweenness = getTopByBetweenness(10);
          this.topByCriticalPath = getTopByCriticalPath(10);
          if (!this.topByEigenvector.length) {
            this.topByEigenvector = getTopByEigenvector(10);
          }
          this.cycleInfo = getCycleInfo(this.triageData);
          this.totalCycles = this.cycleInfo?.cycleCount || 0;
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
            window.bvCharts.init(graphData.issues, graphData.dependencies, getResolvedIssueIDs());
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
            LATEST_TRIAGE_DATA = this.triageData;
            if (!this.graphReady) {
              this.cycleInfo = getCycleInfo(this.triageData);
              this.totalCycles = this.cycleInfo?.cycleCount || 0;
            }
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
          this.graphDetailNode = null;
          if (route.params.id) {
            // Preserve open dependency graph when navigating between issues inside the modal
            const keepDepGraph = Boolean(this.selectedIssue && this.showDepGraph);
            if (!keepDepGraph) {
              this.showDepGraph = false;
              if (this.$refs && this.$refs.depGraph) {
                this.$refs.depGraph.innerHTML = '';
              }
            }
            this.whatIfResult = null;
            this.selectedIssue = getIssue(route.params.id);
            // Update nav list from current issues
            if (this.issues.length) {
              this.issueNavList = this.issues.map(i => i.id);
            }
          }
          this.syncModalOpenBodyClass();
          break;

        case 'issues':
          this.view = 'issues';
          this.graphDetailNode = null;
          if (this.$refs && this.$refs.depGraph) {
            this.$refs.depGraph.innerHTML = '';
          }
          this.selectedIssue = null;
          this.showDepGraph = false;
          this.whatIfResult = null;
          this.syncModalOpenBodyClass();
          this.filters = {
            status: [],
            type: [],
            priority: [],
            labels: [],
            assignee: '',
            hasBlockers: null,
            isBlocking: null,
            criticalPath: false,
            ...urlState.filters,
          };
          if (this.filters.criticalPath && this.graphReady) {
            this.criticalPathData = this.criticalPathData || getCriticalPathSequence();
            this.showCriticalPath = Boolean(this.criticalPathData?.path?.length);
          } else if (!this.filters.criticalPath) {
            this.showCriticalPath = false;
            this.criticalPathData = null;
          }
          this.sort = urlState.sort;
          this.searchQuery = urlState.searchQuery;
          this.searchMode = urlState.searchMode || 'text';
          this.searchPreset = urlState.searchPreset || 'default';
          this.page = 1;
          this.loadIssues();
          break;

        case 'insights':
          this.view = 'insights';
          this.selectedIssue = null;
          this.graphDetailNode = null;
          this.syncModalOpenBodyClass();
          break;

        case 'graph':
          this.view = 'graph';
          this.selectedIssue = null;
          this.syncModalOpenBodyClass();
          this.$nextTick(() => {
            this.initForceGraphView();
          });
          break;

        default:
          this.view = 'dashboard';
          this.selectedIssue = null;
          this.graphDetailNode = null;
          this.syncModalOpenBodyClass();
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
      clearTimeout(this.graphStageTimer);
      this.graphStageTimer = null;
      this.graphLoadingStage = 'init';

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
          this.forceGraphModule = await import('./graph.js' + getViewerAssetQuerySuffix());
        }

        // Seed live physics only when the exported topology matches this database.
        // Missing or stale layouts fall back to ordinary force initialization.
        const precomputedLayout = await this.forceGraphModule.loadPrecomputedLayout(issues, dependencies);

        // Stage 3: Initializing graph visualization
        this.graphLoadingStage = 'init';

        if (!this.forceGraphReady) {
          await this.forceGraphModule.initGraph('graph-container');
          this.forceGraphReady = true;

          // Register event listeners once (inside forceGraphReady check to avoid duplicates)
          // Events are dispatched on document, so listen there
          const handleGraphNodeDetail = (e) => {
            const node = e.detail?.node;
            if (node) {
              this.graphDetailNode = node;
              console.log('[Viewer] Node selected for detail:', node.id);
              // Resize graph after detail pane opens (wait for transition)
              setTimeout(() => this.resizeForceGraph(), 350);
            }
          };
          document.addEventListener('bv-graph:nodeClick', handleGraphNodeDetail);
          document.addEventListener('bv-graph:nodeSelect', handleGraphNodeDetail);
          document.addEventListener('bv-graph:selectionChange', (e) => {
            if (e.detail?.node) {
              if (this.graphDetailNode?.id !== e.detail.node.id) {
                handleGraphNodeDetail(e);
              }
            } else if (e.detail && e.detail.node === null && this.graphDetailNode) {
              this.graphDetailNode = null;
              setTimeout(() => this.resizeForceGraph(), 250);
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

          // Sync transitive dependency cone isolation state
          document.addEventListener('bv-graph:dependencyConeChange', (e) => {
            this.dependencyConeActive = Boolean(e.detail?.active);
            this.dependencyConeFocusId = e.detail?.focusNodeId || null;
            this.dependencyConeCount = Number(e.detail?.count ?? e.detail?.coneCount ?? 0);
          });
          document.addEventListener('bv-graph:dependencyConeReset', () => {
            this.dependencyConeActive = false;
            this.dependencyConeFocusId = null;
            this.dependencyConeCount = 0;
          });

          // Track simulation progress for loading indicator
          document.addEventListener('bv-graph:simulationProgress', (e) => {
            if (this.graphLoadingStage !== 'simulating') return;
            this.graphSimulationProgress = e.detail?.progress ?? 0;
            this.graphSimulationDone = e.detail?.done ?? false;
            if (e.detail?.done && this.graphStageTimer === null) {
              // Schedule once per load. Old tick timers must not hide a newer
              // load's overlay or repeatedly cancel its Alpine transition.
              this.graphStageTimer = setTimeout(() => {
                this.graphStageTimer = null;
                this.graphSimulationProgress = null;
                this.graphLoadingStage = null;
              }, 500);
            }
          });
        }

        // Synchronize Force-Graph palette with current light/dark mode
        this.forceGraphModule.setGraphTheme?.(this.darkMode);

        // Stage 4: Running force simulation
        this.graphLoadingStage = 'simulating';
        this.graphSimulationProgress = 0;
        this.graphSimulationDone = false;

        console.log(`[ForceGraph] Loading ${issues.length} issues, ${dependencies.length} dependencies`);
        this.forceGraphModule.loadData(issues, dependencies, precomputedLayout);

        // Try to load history data for time-travel feature (bv-z38b)
        // Use cache-busting to avoid stale data from CDN
        this.timeTravelAvailable = false;
        this.timeTravelActive = false;
        try {
          // Optional history must not leave graph refresh locked on a stalled
          // response. The deadline also covers consumption of the JSON body.
          const historyResp = await fetch(`./data/history.json?_t=${Date.now()}`, {
            signal: AbortSignal.timeout(3000),
          });
          if (historyResp.ok) {
            const historyData = await historyResp.json();
            if (historyData?.commits?.length > 0 && this.forceGraphModule.initTimeTravel) {
              const ready = this.forceGraphModule.initTimeTravel(historyData);
              this.timeTravelAvailable = Boolean(ready ?? true);
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
        clearTimeout(this.graphStageTimer);
        this.graphStageTimer = null;
        this.graphLoadingStage = null;
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
      if (filters.criticalPath) {
        filters.criticalPathIds = this.criticalPathData?.path || getCriticalPathSequence()?.path || [];
      }

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

      if (this.issues.length === 0) {
        this.focusedIssueIndex = -1;
      } else if (this.focusedIssueIndex >= this.issues.length) {
        this.focusedIssueIndex = this.issues.length - 1;
      }

      // Sync URL state (only on issues view)
      if (this.view === 'issues') {
        syncFiltersToURL('issues', this.filters, this.sort, this.searchQuery, this.searchMode, this.searchPreset);
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
     * Reset all filters cleanly, apply stat-card overrides, navigate to Issues view, and sync URL
     */
    navigateToIssuesWithFilter(overrides = {}) {
      const normalizeArr = (val) => {
        if (val === undefined || val === null || val === '' || val === 'all') return [];
        return Array.isArray(val) ? [...val] : [val];
      };
      const nextFilters = {
        status: normalizeArr(overrides.status),
        type: normalizeArr(overrides.type),
        priority: normalizeArr(overrides.priority),
        labels: Array.isArray(overrides.labels) ? [...overrides.labels] : normalizeArr(overrides.labels),
        assignee: overrides.assignee || '',
        hasBlockers: overrides.hasBlockers === true || overrides.hasBlockers === false ? overrides.hasBlockers : null,
        isBlocking: overrides.isBlocking === true ? true : null,
        criticalPath: Boolean(overrides.criticalPath),
      };
      this.filters = nextFilters;
      if (!nextFilters.criticalPath) {
        this.showCriticalPath = false;
        this.criticalPathData = null;
        this.criticalPathAnimationStep = -1;
      }
      this.searchQuery = '';
      this.selectedIssue = null;
      this.graphDetailNode = null;
      this.page = 1;
      this.view = 'issues';
      this.loadIssues();
    },

    /**
     * Clear critical path filter & highlighting
     */
    clearCriticalPathFilter() {
      this.filters.criticalPath = false;
      this.showCriticalPath = false;
      this.criticalPathData = null;
      this.criticalPathAnimationStep = -1;
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
        criticalPath: false,
      };
      this.showCriticalPath = false;
      this.criticalPathData = null;
      this.criticalPathAnimationStep = -1;
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
             Boolean(this.filters.criticalPath) ||
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
     * Search issues (switches from non-issues views to Issues when typing in global header search)
     */
    search() {
      this.page = 1;
      if (String(this.searchQuery || '').trim().length > 0 && this.view !== 'issues') {
        this.selectedIssue = null;
        this.graphDetailNode = null;
        this.view = 'issues';
      }
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

      const keepDepGraph = Boolean(this.selectedIssue && this.showDepGraph);
      if (!keepDepGraph) {
        this._depGraphRenderSeq = (this._depGraphRenderSeq || 0) + 1;
        this.showDepGraph = false;
        if (this.$refs && this.$refs.depGraph) {
          this.$refs.depGraph.innerHTML = '';
        }
      }
      this.whatIfResult = null;
      this.selectedIssue = issue;

      if (this.issues.length) {
        this.issueNavList = this.issues.map(i => i.id);
      }
    },

    /**
     * Alias for opening an issue modal directly
     */
    openIssue(id) {
      this.selectIssue(id);
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
      this._depGraphRenderSeq = (this._depGraphRenderSeq || 0) + 1;
      if (this.$refs && this.$refs.depGraph) {
        this.$refs.depGraph.innerHTML = '';
      }
      this.selectedIssue = null;
      this.showDepGraph = false;
      this.whatIfResult = null;

      // Navigate back
      const currentView = this.view;
      if (currentView === 'issues') {
        navigateToIssues(this.filters, this.sort, this.searchQuery, this.searchMode, this.searchPreset);
      } else {
        navigate('/' + currentView);
      }
    },

    /**
     * Check whether a keyboard event originated inside an editable input/control
     */
    isEditableEventTarget(event) {
      const target = event?.target || (typeof document !== 'undefined' ? document.activeElement : null);
      if (!target) return false;
      const tag = String(target.tagName || '').toUpperCase();
      return ['INPUT', 'TEXTAREA', 'SELECT'].includes(tag) || Boolean(target.isContentEditable);
    },

    /**
     * Toggle the Mermaid dependency graph inside the issue detail modal (ignoring editable inputs)
     */
    toggleDependencyGraphView(event) {
      if (!this.selectedIssue || this.isEditableEventTarget(event)) return;
      this.showDepGraph = !this.showDepGraph;
    },

    /**
     * Navigate to next/previous issue in the list
     * @param {number} direction - 1 for next, -1 for previous
     * @param {KeyboardEvent} [event] - Optional keyboard event for input guard
     */
    navigateIssue(direction, event) {
      if (!this.selectedIssue || this.isEditableEventTarget(event)) return;

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
      if (!this.showDepGraph && this.$refs && this.$refs.depGraph) {
        this.$refs.depGraph.innerHTML = '';
      }
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
      const targetIssueId = issue.id;
      const renderSeq = (this._depGraphRenderSeq = (this._depGraphRenderSeq || 0) + 1);
      this.$refs.depGraph.innerHTML = '';
      const groups = issue.dependencyGroups || {};
      const blockedByEntries = groups.blockedBy?.length
        ? groups.blockedBy
        : (issue.blocked_by_ids || '').split(',').filter(Boolean).map(s => ({ id: s.trim(), status: 'open', dep_type: 'blocks', available: true }));
      const blocksEntries = groups.blocks?.length
        ? groups.blocks
        : (issue.blocks_ids || '').split(',').filter(Boolean).map(s => ({ id: s.trim(), status: 'open', dep_type: 'blocks', available: true }));
      const parentEntries = groups.parents || [];
      const childEntries = groups.children || [];
      const relatedEntries = groups.related || [];

      if (
        blockedByEntries.length === 0 &&
        blocksEntries.length === 0 &&
        parentEntries.length === 0 &&
        childEntries.length === 0 &&
        relatedEntries.length === 0
      ) {
        this.$refs.depGraph.innerHTML = '<p class="text-gray-500 dark:text-gray-400 text-center">No dependencies</p>';
        return;
      }

      // Build Mermaid flowchart
      let diagram = 'flowchart TB\n';

      // Sanitize ID/labels for Mermaid
      const sanitizeId = (id) => 'n_' + String(id).replace(/[^a-zA-Z0-9]/g, '_');
      const sanitizeLabel = (text) => String(text ?? '').replace(/["\[\]\n\r]/g, ' ').trim();
      const sanitizeEdgeLabel = (text) => String(text ?? '').replace(/[^a-zA-Z0-9_-]/g, '_');
      const currentId = sanitizeId(issue.id);

      // Style for current node
      diagram += `  ${currentId}["${sanitizeLabel(issue.id)}"]\n`;
      diagram += `  style ${currentId} fill:#0ea5e9,stroke:#0284c7,color:#fff\n`;

      const navigableIds = new Set([issue.id]);
      const styledNodes = new Set([currentId]);
      const defineNode = (entry, activeStyle) => {
        const nodeId = sanitizeId(entry.id);
        if (!styledNodes.has(nodeId)) {
          styledNodes.add(nodeId);
          const suffix = entry.status === 'missing' ? ' (missing)' : '';
          diagram += `  ${nodeId}["${sanitizeLabel(entry.id + suffix)}"]\n`;
          const isResolved = entry.status === 'closed' || entry.status === 'tombstone';
          const styleSpec = isResolved
            ? 'fill:#f3f4f6,stroke:#9ca3af,color:#6b7280'
            : entry.status === 'missing'
              ? 'fill:#fef2f2,stroke:#ef4444,stroke-dasharray: 4 2'
              : activeStyle;
          diagram += `  style ${nodeId} ${styleSpec}\n`;
        }
        if (entry.available !== false) {
          navigableIds.add(entry.id);
        }
        return nodeId;
      };

      // Upstream blockers (including closed/missing)
      for (const dep of blockedByEntries) {
        const nodeId = defineNode(dep, 'fill:#fef3c7,stroke:#f59e0b');
        diagram += `  ${nodeId} --> ${currentId}\n`;
      }

      // Downstream dependents (including closed)
      for (const dep of blocksEntries) {
        const nodeId = defineNode(dep, 'fill:#fee2e2,stroke:#ef4444');
        diagram += `  ${currentId} --> ${nodeId}\n`;
      }

      // Parents & children (hierarchical non-blocking)
      for (const dep of parentEntries) {
        const nodeId = defineNode(dep, 'fill:#e0e7ff,stroke:#6366f1');
        diagram += `  ${nodeId} -. parent .-> ${currentId}\n`;
      }
      for (const dep of childEntries) {
        const nodeId = defineNode(dep, 'fill:#e0e7ff,stroke:#6366f1');
        diagram += `  ${currentId} -. child .-> ${nodeId}\n`;
      }

      // Related / discovered-from / custom non-blocking
      for (const dep of relatedEntries) {
        const nodeId = defineNode(dep, 'fill:#e0f2fe,stroke:#0284c7');
        const edgeLabel = sanitizeEdgeLabel(dep.dep_type || 'related');
        if (dep.direction === 'incoming') {
          diagram += `  ${nodeId} -. ${edgeLabel} .-> ${currentId}\n`;
        } else {
          diagram += `  ${currentId} -. ${edgeLabel} .-> ${nodeId}\n`;
        }
      }

      // Add click handlers for available issues
      for (const id of navigableIds) {
        const nodeId = sanitizeId(id);
        const safeId = String(id).replace(/\\/g, '\\\\').replace(/"/g, '\\"');
        diagram += `  click ${nodeId} call window.beadsViewer.navigateToIssue("${safeId}")\n`;
      }

      try {
        // Render the diagram
        const { svg } = await mermaid.render('dep-graph-' + nextToastId().replace(/[^a-zA-Z0-9]/g, '_'), diagram);
        if (this._depGraphRenderSeq !== renderSeq || !this.showDepGraph || !this.selectedIssue || this.selectedIssue.id !== targetIssueId || !this.$refs.depGraph) return;
        this.$refs.depGraph.innerHTML = svg;
      } catch (err) {
        console.warn('Mermaid render failed:', err);
        if (this._depGraphRenderSeq === renderSeq && this.showDepGraph && this.selectedIssue && this.selectedIssue.id === targetIssueId && this.$refs.depGraph) {
          this.$refs.depGraph.innerHTML = '<p class="text-red-500 text-center text-sm">Failed to render graph</p>';
        }
      }
    },

    /**
     * Toggle dark mode
     */
    toggleDarkMode() {
      this.darkMode = !this.darkMode;
      localStorage.setItem('darkMode', this.darkMode);
      document.documentElement.classList.toggle('dark', this.darkMode);
      window.BVCharts?.applyTheme?.(this.darkMode);
      this.forceGraphModule?.setGraphTheme?.(this.darkMode);

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
      return this.includeBlockedWhatIf ? allWhatIf(limit) : topWhatIf(limit);
    },

    /**
     * Toggle including blocked open issues in Insights Cascade Impact Analysis
     */
    toggleIncludeBlockedWhatIf() {
      this.includeBlockedWhatIf = !this.includeBlockedWhatIf;
      if (!this.graphReady) return;
      this.topImpactIssues = this.includeBlockedWhatIf ? allWhatIf(10) : topWhatIf(10);
    },

    async highlightCriticalPathInGraph() {
      const ready = await this.initForceGraph();
      if (!ready) return;
      this.forceGraphModule?.toggleCriticalPath?.();
    },

    async prevCriticalPathInGraph() {
      const ready = await this.initForceGraph();
      if (!ready) return;
      this.forceGraphModule?.prevCriticalPath?.();
    },

    async nextCriticalPathInGraph() {
      const ready = await this.initForceGraph();
      if (!ready) return;
      this.forceGraphModule?.nextCriticalPath?.();
    },

    async focusCriticalPathInGraph() {
      const ready = await this.initForceGraph();
      if (!ready) return;
      this.forceGraphModule?.zoomToCriticalPath?.();
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
          this.filters.criticalPath = false;
          return;
        }
        this.filters.criticalPath = true;
        showToast(`Critical path: ${this.criticalPathData.length} issues deep`, 'success');
        if (this.view === 'issues') {
          this.applyFilter();
        }
        // Start animation
        this.animateCriticalPath();
      } else {
        this.filters.criticalPath = false;
        this.criticalPathData = null;
        this.criticalPathAnimationStep = -1;
        if (this.view === 'issues') {
          this.applyFilter();
        }
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
     * @param {string} presetName - One of: 'force', 'compact', 'spread', 'orthogonal', 'radial', 'cluster', 'galaxy'
     */
    applyGraphPreset(presetName) {
      if (!this.forceGraphModule) {
        showToast('Graph not initialized', 'warning');
        return;
      }
      if (this.forceGraphModule.applyPreset) {
        const success = this.forceGraphModule.applyPreset(presetName);
        if (success) {
          this.currentPreset = presetName;
          const presets = this.forceGraphModule.getLayoutPresets?.() || {};
          const preset = presets[presetName];
          showToast(`Layout: ${preset?.name || presetName}`, 'info');
        }
      } else if (presetName === 'galaxy' && this.forceGraphModule.setViewMode) {
        this.forceGraphModule.setViewMode('galaxy');
        this.currentPreset = 'galaxy';
      } else {
        showToast('Presets not available', 'warning');
      }
    },

    /**
     * Toggle Git history time-travel playback controls in the graph view
     */
    toggleTimeTravel() {
      if (!this.forceGraphModule) {
        showToast('Graph not initialized', 'warning');
        return;
      }
      if (typeof this.forceGraphModule.toggleTimeTravel === 'function') {
        this.timeTravelActive = Boolean(this.forceGraphModule.toggleTimeTravel());
      } else if (this.forceGraphModule.isTimeTravelActive?.()) {
        this.forceGraphModule.stopTimeTravel?.();
        this.timeTravelActive = false;
      } else {
        this.forceGraphModule.startTimeTravel?.();
        this.timeTravelActive = true;
      }
    },

    /**
     * Ensure graph view is active and ForceGraph is initialized
     */
    async initForceGraph() {
      if (this.view !== 'graph') {
        this.view = 'graph';
        this.selectedIssue = null;
        if (window.location.hash !== '#/graph') {
          window.history.replaceState(null, '', '#/graph');
        }
        await new Promise(resolve => this.$nextTick(resolve));
      }
      if (this.forceGraphLoading) {
        while (this.forceGraphLoading) {
          await new Promise(resolve => setTimeout(resolve, 25));
        }
      } else if (!this.forceGraphReady || !this.forceGraphModule) {
        await this.initForceGraphView();
      }
      return Boolean(this.forceGraphReady && this.forceGraphModule);
    },

    async highlightCyclesInGraph() {
      const ready = await this.initForceGraph();
      if (!ready) return;
      this.forceGraphModule?.toggleCycleNavigator?.();
    },

    async prevCycleInGraph() {
      const ready = await this.initForceGraph();
      if (!ready) return;
      if (!this.cycleNavigatorActive) {
        this.forceGraphModule?.toggleCycleNavigator?.();
      }
      this.forceGraphModule?.prevCycle?.();
    },

    async nextCycleInGraph() {
      const ready = await this.initForceGraph();
      if (!ready) return;
      if (!this.cycleNavigatorActive) {
        this.forceGraphModule?.toggleCycleNavigator?.();
      }
      this.forceGraphModule?.nextCycle?.();
    },

    async focusCycleInGraph() {
      const ready = await this.initForceGraph();
      if (!ready) return;
      this.forceGraphModule?.zoomToCycle?.();
    },

    /**
     * Set single-select label filter from dropdown
     */
    setLabelFilter(label) {
      const trimmed = String(label || '').trim();
      this.filters.labels = trimmed ? [trimmed] : [];
      this.applyFilter();
    },

    /**
     * Toggle a label in filters.labels (clicking label badge on card/detail/graph)
     */
    toggleLabelFilter(label) {
      const trimmed = String(label || '').trim();
      if (!trimmed) return;
      if (!Array.isArray(this.filters.labels)) {
        this.filters.labels = [];
      }
      const idx = this.filters.labels.indexOf(trimmed);
      if (idx === -1) {
        this.filters.labels.push(trimmed);
      } else {
        this.filters.labels.splice(idx, 1);
      }
      this.selectedIssue = null;
      this.graphDetailNode = null;
      this.page = 1;
      if (this.view !== 'issues') {
        navigateToIssues(this.filters, this.sort, this.searchQuery, this.searchMode, this.searchPreset);
      } else {
        this.loadIssues();
      }
    },

    /**
     * Invoke an error recovery action ({ action }, { handler }, or { url })
     */
    runErrorAction(action) {
      if (!action) return;
      if (typeof action.action === 'function') {
        action.action();
      } else if (typeof action.handler === 'function') {
        action.handler();
      } else if (action.url) {
        window.open(action.url, '_blank', 'noopener,noreferrer');
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
        eigenvector: 'Eigenvector Centrality',
        inDegree: 'In-Degree (Dependents)',
        indegree: 'In-Degree (Dependents)',
        outDegree: 'Out-Degree (Blockers)',
        criticalPath: 'Critical Path Depth',
        critical: 'Critical Path Depth',
        kcore: 'K-Core Number',
        hitsHub: 'HITS Hub Score',
        hitsAuth: 'HITS Authority Score',
        slack: 'Schedule Slack',
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
    formatDateFull,
    formatEstimatedMinutes,
    formatCriticalPathHops,

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
    isIssueOverdue,
    statusBadgeClass,
    getScoreBreakdownBars(breakdown) {
      return getScoreBreakdownBars(breakdown, this.getEffectiveTriageWeights());
    },

    getEffectiveTriageWeights() {
      return resolveEffectiveTriageWeights(this.triageData);
    },

    getTriageWeightComponents() {
      const w = this.getEffectiveTriageWeights();
      return [
        { key: 'pagerank', label: '🎯 PageRank', barClass: 'bg-blue-500', weight: w.pagerank },
        { key: 'betweenness', label: '🔀 Between.', barClass: 'bg-orange-500', weight: w.betweenness },
        { key: 'blocker_ratio', label: '🚧 Blockers', barClass: 'bg-red-500', weight: w.blocker_ratio },
        { key: 'priority_boost', label: '⭐ Priority', barClass: 'bg-yellow-500', weight: w.priority_boost },
        { key: 'time_to_impact', label: '⚡ Time', barClass: 'bg-cyan-500', weight: w.time_to_impact },
        { key: 'urgency', label: '🔥 Urgency', barClass: 'bg-rose-500', weight: w.urgency },
        { key: 'risk', label: '⚠️ Risk', barClass: 'bg-purple-500', weight: w.risk },
        { key: 'staleness', label: '⏰ Staleness', barClass: 'bg-gray-400', weight: w.staleness },
      ].map(item => {
        const weight = Number(item.weight) || 0;
        const pct = Math.max(0, Math.min(100, Math.round(weight * 100)));
        return {
          ...item,
          weight,
          value: weight,
          pct,
          pctText: `${pct}%`,
        };
      });
    },

    getTriageAlerts() {
      const raw = this.triageData?.alerts ?? this.triageData?.triage?.alerts;
      if (!Array.isArray(raw)) return [];
      return raw.map((item, idx) => {
        if (typeof item === 'string') {
          return {
            key: `alert-${idx}`,
            severity: 'warning',
            type: 'alert',
            message: item,
            issue_id: null,
          };
        }
        return {
          key: item?.id || `${item?.type || 'alert'}-${item?.issue_id || idx}-${idx}`,
          severity: String(item?.severity || 'warning').toLowerCase(),
          type: item?.type || item?.kind || 'alert',
          message: item?.message || item?.summary || item?.description || String(item?.type || 'Alert'),
          issue_id: item?.issue_id || item?.bead_id || null,
        };
      });
    },

    getTriageSourceAuthority() {
      return this.triageData?.source_authority ?? this.triageData?.triage?.source_authority ?? null;
    },

    hasProvisionalSourceAuthority() {
      const sa = this.getTriageSourceAuthority();
      return Boolean(sa && sa.claim_safe === false);
    },

    getTriageQuickWins() {
      const raw = this.triageData?.quick_wins ?? this.triageData?.triage?.quick_wins;
      if (Array.isArray(raw)) return raw;
      return this.quickWins || [];
    },

    getTriageBlockersToClear() {
      const raw = this.triageData?.blockers_to_clear ?? this.triageData?.triage?.blockers_to_clear;
      if (Array.isArray(raw)) return raw;
      return this.blockersToClose || [];
    },

    getTriageRecommendations() {
      const raw = this.triageData?.recommendations ?? this.triageData?.triage?.recommendations;
      return Array.isArray(raw) ? raw : [];
    },

    getTriageRecommendationsByTrack() {
      const raw = this.triageData?.recommendations_by_track ?? this.triageData?.triage?.recommendations_by_track;
      return Array.isArray(raw) ? raw : [];
    },

    getTriageRecommendationsByLabel() {
      const raw = this.triageData?.recommendations_by_label ?? this.triageData?.triage?.recommendations_by_label;
      return Array.isArray(raw) ? raw : [];
    },

    getIssueActionCommands(issueId) {
      if (!issueId) return null;
      const rec = this.getTriageRecommendations().find(r => r && r.id === issueId);
      const actions = rec?.actions || null;
      if (!actions) return null;
      if (this.hasProvisionalSourceAuthority()) {
        if (!actions.show) return null;
        const { claim: _claim, ...rest } = actions;
        return rest;
      }
      return actions;
    },

    async copyShellCommand(cmd) {
      const text = String(cmd || '').trim();
      if (!text) return false;
      this.copiedCommandText = text;
      try {
        await navigator.clipboard.writeText(text);
        showToast('Command copied', 'info');
        return true;
      } catch {
        showToast('Select command to copy manually', 'warning');
        return false;
      }
    },

    async focusNodeDependencyCone(nodeId) {
      if (!nodeId) return false;
      const ready = await this.initForceGraph();
      if (!ready) return false;
      if (this.dependencyConeActive && this.dependencyConeFocusId === nodeId) {
        this.forceGraphModule?.resetDependencyCone?.();
        return false;
      }
      return Boolean(this.forceGraphModule?.focusDependencyCone?.(nodeId));
    },

    resetGraphDependencyCone() {
      this.forceGraphModule?.resetDependencyCone?.();
    },

    toggleMobileTooltip(event) {
      const trigger = event?.currentTarget?.closest?.('.metric-tooltip-trigger')
        || event?.target?.closest?.('.metric-tooltip-trigger');
      if (!trigger) return;
      const tooltip = trigger.querySelector('.metric-tooltip');
      if (!tooltip) return;
      const nextOpen = !tooltip.classList.contains('tooltip-visible');
      document.querySelectorAll('.metric-tooltip.tooltip-visible').forEach(el => {
        if (el !== tooltip) el.classList.remove('tooltip-visible');
      });
      document.querySelectorAll('.metric-tooltip-trigger.tooltip-open').forEach(el => {
        if (el !== trigger) el.classList.remove('tooltip-open');
      });
      trigger.classList.toggle('tooltip-open', nextOpen);
      tooltip.classList.toggle('tooltip-visible', nextOpen);
    },

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
  supportsJsonEach,
  formatEstimatedMinutes,
  formatCriticalPathHops,
  loadDatabase,
  execQuery,
  queryIssues,
  countIssues,
  getIssue,
  getIssueDependencies,
  getIssueDependencyGroups,
  isIssueOverdue,
  statusBadgeClass,
  getReferenceNowMs,
  resolveEffectiveTriageWeights,
  getScoreBreakdownBars,
  getStats,
  getMeta,
  getFilterOptions,
  getUniqueLabels,
  searchIssues,
  getMaxBlocksCount,
  getViewerAssetQuerySuffix,

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
  buildCandidateSet,
  getResolvedIssueIDs,
  recalculateMetrics,
  whatIfClose,
  whatIfCloseBatch,
  topWhatIf,
  allWhatIf,
  getParallelCutData,
  getBridgeDependencies,
  getTopCriticalPaths,
  getActionableIssues,
  getCycleBreakSuggestions,
  getCriticalPathSequence,
  getTopKSet,
  getCoverageSet,

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
  buildFtsMatchQuery,
  formatDate,
  getTopByTriageScore,
  getTopBlockers,
  getTopByBetweenness,
  getTopByKCore,
  getArticulationPoints,
  getTopByCriticalPath,
  getTopByEigenvector,
  getIssuesBySlack,
  getCycleInfo,

  // Error handling
  ERROR_STATE,
  DIAGNOSTICS,
  showError,
  clearError,
  safeQuery,
  showToast,
};
