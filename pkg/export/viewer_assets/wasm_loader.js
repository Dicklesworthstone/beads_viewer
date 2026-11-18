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
  switch (String(status || '').toLowerCase().trim()) {
    case 'open':
      return 0;
    case 'in_progress':
      return 1;
    case 'blocked':
      return 2;
    case 'closed':
      return 3;
    case 'review':
      return 4;
    case 'hooked':
      return 5;
    case 'pinned':
      return 6;
    case 'deferred':
      return 7;
    case 'draft':
      return 8;
    case 'tombstone':
      return 9;
    default:
      return 255;
  }
}

function priorityToCode(priority) {
  if (typeof priority === 'number' && Number.isInteger(priority) && priority >= 0 && priority <= 4) {
    return priority;
  }
  if (typeof priority === 'string' && /^[0-4]$/.test(priority.trim())) {
    return Number(priority.trim());
  }
  return 255;
}

function isTruthyHybridFlag(val) {
  return val === true || val === 'true' || Number(val) > 0;
}

function resolveHybridEffectiveStatus(r, refNowMs) {
  const normStatus = String(r?.status || '').trim().toLowerCase();
  if (normStatus === 'closed' || normStatus === 'tombstone') {
    return normStatus;
  }
  if (
    isTruthyHybridFlag(r?.is_template ?? r?.isTemplate) ||
    isTruthyHybridFlag(r?.pinned ?? r?.isPinned) ||
    isTruthyHybridFlag(r?.ephemeral ?? r?.isEphemeral) ||
    normStatus === 'pinned'
  ) {
    return 'deferred';
  }
  const deferRaw = r?.defer_until ?? r?.deferUntil;
  if ((normStatus === 'open' || normStatus === 'in_progress') && deferRaw) {
    const deferMs = new Date(deferRaw).getTime();
    if (Number.isFinite(deferMs) && deferMs > refNowMs) {
      return 'deferred';
    }
  }
  const depState = String(r?.dependency_state ?? r?.dependencyState ?? '').trim().toLowerCase();
  const isDepBlocked = (depState !== '' && depState !== 'satisfied') || Number(r?.open_blocker_count ?? 0) > 0;
  if ((normStatus === 'open' || normStatus === 'in_progress') && isDepBlocked) {
    return 'blocked';
  }
  return normStatus;
}

function resolveHybridReferenceNowMs() {
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

function daysSince(updatedAt, refNowMs = resolveHybridReferenceNowMs()) {
  if (!updatedAt) return -1;
  const ts = new Date(updatedAt).getTime();
  if (!Number.isFinite(ts)) return -1;
  return Math.max(0, (refNowMs - ts) / (1000 * 60 * 60 * 24));
}

async function resolveHasHybridWasm(metaOverride) {
  const pickFlag = (m) => {
    if (!m || typeof m !== 'object') return undefined;
    if (typeof m.has_hybrid_wasm === 'boolean') return m.has_hybrid_wasm;
    if (m.has_hybrid_wasm === 'true') return true;
    if (m.has_hybrid_wasm === 'false') return false;
    return undefined;
  };

  const direct = pickFlag(metaOverride)
    ?? pickFlag(typeof window !== 'undefined' ? window.BV_META : null)
    ?? pickFlag(typeof window !== 'undefined' ? window.beadsViewer?.getMeta?.() : null);
  if (direct !== undefined) {
    return direct;
  }

  if (typeof fetch === 'function') {
    try {
      const resp = await fetch('./data/meta.json');
      if (resp.ok) {
        const json = await resp.json();
        if (typeof window !== 'undefined') {
          window.BV_META = json;
        }
        return json?.has_hybrid_wasm === true || json?.has_hybrid_wasm === 'true';
      }
    } catch (_err) {
      // Ignore metadata fetch errors; treat optional hybrid WASM as absent
    }
  }

  return false;
}

async function initHybridWasmScorer(issueCount, meta = null) {
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

  const hasHybridWasm = await resolveHasHybridWasm(meta);
  if (hasHybridWasm !== true) {
    HYBRID_WASM_STATE.reason = 'Optional hybrid WASM module not bundled';
    return false;
  }

  try {
    const suffix = (typeof getViewerAssetQuerySuffix === 'function')
      ? getViewerAssetQuerySuffix()
      : (typeof window !== 'undefined' && typeof window.beadsViewer?.getViewerAssetQuerySuffix === 'function'
        ? window.beadsViewer.getViewerAssetQuerySuffix()
        : '');
    const wasmModule = await import('./wasm/bv_hybrid_scorer.js' + suffix);
    if (suffix && typeof window !== 'undefined' && window.location?.href) {
      await wasmModule.default({
        module_or_path: new URL('./wasm/bv_hybrid_scorer_bg.wasm' + suffix, window.location.href),
      });
    } else {
      await wasmModule.default();
    }
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

function scoreBatchHybridFallback(results, weights, options = {}) {
  if (typeof HybridScorer === 'undefined') {
    return results;
  }
  const scorer = new HybridScorer(weights, options);
  return scorer.scoreAndRank(results);
}

function scoreBatchHybrid(results, weights, options = {}) {
  if (!Array.isArray(results) || results.length === 0) {
    return [];
  }

  const configuredMax = (
    typeof options?.maxBlockerCount === 'number' &&
    Number.isFinite(options.maxBlockerCount) &&
    options.maxBlockerCount >= 0
  ) ? Math.max(0, toInt(options.maxBlockerCount, 0)) : null;

  if (!HybridWasmCtor) {
    return scoreBatchHybridFallback(results, weights, options);
  }

  const refNowMs = resolveHybridReferenceNowMs();
  const payload = results.map(r => ({
    id: String(r.id ?? r.issue_id ?? ''),
    text_score: toNumber(r.textScore ?? r.text_score, 0),
    pagerank: (typeof r.pagerank === 'number' && Number.isFinite(r.pagerank)) ? r.pagerank : 0.5,
    status: statusToCode(resolveHybridEffectiveStatus(r, refNowMs)),
    priority: priorityToCode(r.priority),
    blocker_count: Math.max(0, toInt(r.blockerCount ?? r.impact ?? r.blocks_count ?? r.dependent_count ?? 0, 0)),
    days_since_update: daysSince(r.updatedAt ?? r.updated_at, refNowMs),
  }));

  let scorer;
  try {
    const scorerConfig = configuredMax !== null
      ? { ...weights, max_blocker_count: configuredMax }
      : weights;
    scorer = new HybridWasmCtor(JSON.stringify(scorerConfig));
    const raw = (configuredMax !== null && typeof scorer.score_batch_with_max === 'function')
      ? scorer.score_batch_with_max(JSON.stringify(payload), configuredMax)
      : scorer.score_batch(JSON.stringify(payload));
    const parsed = JSON.parse(raw);
    if (!Array.isArray(parsed) || parsed.length === 0) {
      return scoreBatchHybridFallback(results, weights, options);
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
      return scoreBatchHybridFallback(results, weights, options);
    }
    return merged;
  } catch (err) {
    console.warn('[HybridScorer] WASM scoring failed, using JS fallback', err);
    return scoreBatchHybridFallback(results, weights, options);
  } finally {
    if (scorer && typeof scorer.free === 'function') {
      scorer.free();
    }
  }
}

function __setHybridWasmCtorForTest(ctor) {
  HybridWasmCtor = ctor || null;
  HYBRID_WASM_STATE.ready = Boolean(HybridWasmCtor);
}

if (typeof window !== 'undefined') {
  window.initHybridWasmScorer = initHybridWasmScorer;
  window.scoreBatchHybrid = scoreBatchHybrid;
  window.getHybridWasmStatus = getHybridWasmStatus;
}
if (typeof module !== 'undefined' && module.exports) {
  module.exports = {
    initHybridWasmScorer,
    scoreBatchHybrid,
    getHybridWasmStatus,
    resolveHybridEffectiveStatus,
    statusToCode,
    __setHybridWasmCtorForTest,
  };
}
