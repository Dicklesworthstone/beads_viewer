const HYBRID_WASM_STATE = {
  ready: false,
  attempted: false,
  reason: null,
  threshold: 5000,
};

let HybridWasmCtor = null;

function getHybridWasmStatus() {
  return { ...HYBRID_WASM_STATE };
}

function toNumber(value, fallback = 0) {
  const n = Number(value);
  return Number.isFinite(n) ? n : fallback;
}

function toInt(value, fallback = 0) {
  const n = Number(value);
  if (!Number.isFinite(n)) return fallback;
  return Math.trunc(n);
}

function statusToCode(status) {
  switch (status) {
    case 'open':
      return 0;
    case 'in_progress':
      return 1;
    case 'blocked':
      return 2;
    case 'closed':
      return 3;
    default:
      return 255;
  }
}

function daysSince(updatedAt) {
  if (!updatedAt) return -1;
  const ts = new Date(updatedAt).getTime();
  if (!Number.isFinite(ts)) return -1;
  return (Date.now() - ts) / (1000 * 60 * 60 * 24);
}

async function initHybridWasmScorer(issueCount) {
  if (HybridWasmCtor) {
    HYBRID_WASM_STATE.ready = true;
    return true;
  }

  if (HYBRID_WASM_STATE.attempted) {
    return HYBRID_WASM_STATE.ready;
  }
  HYBRID_WASM_STATE.attempted = true;

  if (typeof issueCount === 'number' && issueCount < HYBRID_WASM_STATE.threshold) {
    HYBRID_WASM_STATE.reason = 'dataset below threshold';
    return false;
  }

  if (typeof WebAssembly !== 'object') {
    HYBRID_WASM_STATE.reason = 'WebAssembly not available';
    return false;
  }

  try {
    const wasmModule = await import('./wasm/bv_hybrid_scorer.js');
    await wasmModule.default();
    HybridWasmCtor = wasmModule.HybridScorer;
    HYBRID_WASM_STATE.ready = true;
    HYBRID_WASM_STATE.reason = null;
    console.log('[HybridScorer] WASM loaded');
    return true;
  } catch (err) {
    HYBRID_WASM_STATE.reason = err?.message || 'Failed to load WASM module';
    console.warn('[HybridScorer] WASM unavailable, using JS fallback', err);
    return false;
  }
}

function scoreBatchHybridFallback(results, weights) {
  if (typeof HybridScorer === 'undefined') {
    return results;
  }
  const scorer = new HybridScorer(weights);
  return scorer.scoreAndRank(results);
}

function scoreBatchHybrid(results, weights) {
  if (!Array.isArray(results) || results.length === 0) {
    return [];
  }

  if (!HybridWasmCtor) {
    return scoreBatchHybridFallback(results, weights);
  }

  const payload = results.map(r => ({
    id: String(r.id ?? r.issue_id ?? ''),
    text_score: toNumber(r.textScore ?? r.text_score, 0),
    pagerank: toNumber(r.pagerank, 0.5),
    status: statusToCode(r.status),
    priority: Math.max(0, Math.min(4, toInt(r.priority, 2))),
    blocker_count: Math.max(0, toInt(r.blockerCount ?? r.blocker_count, 0)),
    days_since_update: daysSince(r.updatedAt ?? r.updated_at),
  }));

  let scorer;
  try {
    scorer = new HybridWasmCtor(JSON.stringify(weights));
    const raw = scorer.score_batch(JSON.stringify(payload));
    const parsed = JSON.parse(raw);
    if (!Array.isArray(parsed) || parsed.length === 0) {
      return scoreBatchHybridFallback(results, weights);
    }

    const byId = new Map(
      results.map(r => [String(r.id ?? r.issue_id ?? ''), r])
    );
    const merged = [];
    for (const item of parsed) {
      const base = byId.get(item.id);
      if (!base) continue;
      merged.push({
        ...base,
        hybrid_score: toNumber(item.score, 0),
        component_scores: {
          pagerank: toNumber(item.components?.[0], 0.5),
          status: toNumber(item.components?.[1], 0.5),
          impact: toNumber(item.components?.[2], 0.5),
          priority: toNumber(item.components?.[3], 0.5),
          recency: toNumber(item.components?.[4], 0.5),
        },
      });
    }
    if (merged.length !== results.length) {
      return scoreBatchHybridFallback(results, weights);
    }
    return merged;
  } catch (err) {
    console.warn('[HybridScorer] WASM scoring failed, using JS fallback', err);
    return scoreBatchHybridFallback(results, weights);
  } finally {
    if (scorer && typeof scorer.free === 'function') {
      scorer.free();
    }
  }
}

window.initHybridWasmScorer = initHybridWasmScorer;
window.scoreBatchHybrid = scoreBatchHybrid;
window.getHybridWasmStatus = getHybridWasmStatus;
