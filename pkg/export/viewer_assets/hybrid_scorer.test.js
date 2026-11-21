/* eslint-disable no-console */
(function runHybridScorerTests() {
  let ScorerClass = typeof HybridScorer !== 'undefined' ? HybridScorer : undefined;
  let Presets = typeof HYBRID_PRESETS !== 'undefined' ? HYBRID_PRESETS : undefined;
  let scorerHelpers = {
    computeActivePageRankBounds: typeof computeActivePageRankBounds === 'function' ? computeActivePageRankBounds : undefined,
    resolveHybridPageRankMax: typeof resolveHybridPageRankMax === 'function' ? resolveHybridPageRankMax : undefined,
    normalizeHybridPageRank: typeof normalizeHybridPageRank === 'function' ? normalizeHybridPageRank : undefined,
  };
  let wasmLoader = null;

  if (typeof require === 'function') {
    const mod = require('./hybrid_scorer.js');
    if (!ScorerClass || !Presets) {
      ScorerClass = mod.HybridScorer;
      Presets = mod.HYBRID_PRESETS;
      globalThis.HybridScorer = ScorerClass;
      globalThis.HYBRID_PRESETS = Presets;
    }
    scorerHelpers = {
      computeActivePageRankBounds: mod.computeActivePageRankBounds,
      resolveHybridPageRankMax: mod.resolveHybridPageRankMax,
      normalizeHybridPageRank: mod.normalizeHybridPageRank,
    };
    wasmLoader = require('./wasm_loader.js');
  }

  if (typeof ScorerClass === 'undefined' || typeof Presets === 'undefined') {
    console.warn('HybridScorer not available; skipping tests.');
    return;
  }

  function assertClose(actual, expected, tolerance, message) {
    if (Math.abs(actual - expected) > tolerance) {
      throw new Error(`${message}: got ${actual}, want ${expected}`);
    }
  }

  function verifyBaselineScore() {
    const scorer = new ScorerClass(Presets.default);
    const now = new Date();
    const result = scorer.scoreAndRank([{
      id: 'test-1',
      textScore: 0.8,
      pagerank: 0.5,
      status: 'open',
      priority: 1,
      blockerCount: 3,
      updatedAt: now.toISOString(),
    }])[0];

    if (!result.component_scores) {
      throw new Error('component_scores missing from result');
    }

    // Expected score computed from weights + normalization.
    const statusScore = 1.0;
    const priorityScore = 0.8;
    const impactScore = 1.0; // maxBlockerCount = 3 from scoreAndRank
    const recencyScore = 1.0;
    const expected =
      0.4 * 0.8 +
      0.2 * 0.5 +
      0.15 * statusScore +
      0.1 * impactScore +
      0.1 * priorityScore +
      0.05 * recencyScore;

    assertClose(result.hybrid_score, expected, 0.01, 'hybrid score mismatch');
  }

  function verifyEphemeralParityAndControls() {
    const scorer = new ScorerClass(Presets.default);
    const nowIso = new Date().toISOString();
    const base = {
      textScore: 0.8,
      pagerank: 0.5,
      priority: 1,
      blockerCount: 2,
      updatedAt: nowIso,
    };

    const rows = scorer.scoreAndRank([
      { ...base, id: 'open-control', status: 'open', ephemeral: false },
      { ...base, id: 'open-zero-control', status: 'open', ephemeral: 0 },
      { ...base, id: 'ephemeral-bool', status: 'open', ephemeral: true },
      { ...base, id: 'ephemeral-int', status: 'in_progress', ephemeral: 1 },
      { ...base, id: 'closed-ephemeral-control', status: 'closed', ephemeral: true },
      { ...base, id: 'tombstone-ephemeral-control', status: 'tombstone', ephemeral: 1 },
    ]);
    const byId = new Map(rows.map(r => [r.id, r]));

    // Positive observables: open & in_progress ephemeral issues downgrade to 0.2 ('deferred').
    assertClose(byId.get('ephemeral-bool').component_scores.status, 0.2, 1e-9, 'ephemeral:true status score');
    assertClose(byId.get('ephemeral-int').component_scores.status, 0.2, 1e-9, 'ephemeral:1 status score');

    // Negative controls: non-ephemeral open stays 1.0; closed stays 0.1 and tombstone stays 0.0 (never inflated to 0.2).
    assertClose(byId.get('open-control').component_scores.status, 1.0, 1e-9, 'non-ephemeral open control');
    assertClose(byId.get('open-zero-control').component_scores.status, 1.0, 1e-9, 'ephemeral:0 open control');
    assertClose(byId.get('closed-ephemeral-control').component_scores.status, 0.1, 1e-9, 'closed ephemeral control');
    assertClose(byId.get('tombstone-ephemeral-control').component_scores.status, 0.0, 1e-9, 'tombstone ephemeral control');

    const expectedDelta = 0.15 * (1.0 - 0.2);
    assertClose(
      byId.get('open-control').hybrid_score - byId.get('ephemeral-bool').hybrid_score,
      expectedDelta,
      1e-9,
      'ephemeral hybrid_score delta vs open control'
    );

    if (wasmLoader && typeof wasmLoader.scoreBatchHybrid === 'function') {
      const nowMs = Date.parse(nowIso);
      const assertEq = (actual, expected, msg) => {
        if (actual !== expected) {
          throw new Error(`${msg}: got ${JSON.stringify(actual)}, want ${JSON.stringify(expected)}`);
        }
      };

      // [C-3a] Direct unit verification of wasm_loader.js:resolveHybridEffectiveStatus
      assertEq(wasmLoader.resolveHybridEffectiveStatus({ status: 'open', ephemeral: true }, nowMs), 'deferred', 'resolveHybridEffectiveStatus open ephemeral:true');
      assertEq(wasmLoader.resolveHybridEffectiveStatus({ status: 'in_progress', ephemeral: 1 }, nowMs), 'deferred', 'resolveHybridEffectiveStatus in_progress ephemeral:1');
      assertEq(wasmLoader.resolveHybridEffectiveStatus({ status: 'open', ephemeral: false }, nowMs), 'open', 'resolveHybridEffectiveStatus non-ephemeral open');
      assertEq(wasmLoader.resolveHybridEffectiveStatus({ status: 'closed', ephemeral: true }, nowMs), 'closed', 'resolveHybridEffectiveStatus closed ephemeral');
      assertEq(wasmLoader.resolveHybridEffectiveStatus({ status: 'tombstone', ephemeral: 1 }, nowMs), 'tombstone', 'resolveHybridEffectiveStatus tombstone ephemeral');

      // [C-3b] Non-fallback WASM path of scoreBatchHybrid via mock HybridWasmScorer constructor
      // (temporarily removing globalThis.HybridScorer so JS fallback cannot mask failures!)
      const batchInput = [
        { ...base, id: 'wasm-open-ctrl', status: 'open', ephemeral: 0 },
        { ...base, id: 'wasm-eph-bool', status: 'open', ephemeral: true },
        { ...base, id: 'wasm-eph-inprog', status: 'in_progress', ephemeral: 1 },
        { ...base, id: 'wasm-closed-eph', status: 'closed', ephemeral: true },
        { ...base, id: 'wasm-tomb-eph', status: 'tombstone', ephemeral: 1 },
      ];
      let capturedPayload = null;
      let freedCount = 0;
      const CODE_TO_STATUS_SCORE = {
        0: 1.0, // open
        1: 0.8, // in_progress
        2: 0.5, // blocked
        3: 0.1, // closed
        6: 0.2, // pinned
        7: 0.2, // deferred (ephemeral/template/pinned downgrade target)
        9: 0.0, // tombstone
      };
      class MockHybridWasmScorer {
        constructor(_cfgJson) {}
        score_batch(payloadJson) {
          capturedPayload = JSON.parse(payloadJson);
          return JSON.stringify(capturedPayload.map(item => {
            const stScore = CODE_TO_STATUS_SCORE[item.status] ?? 0.5;
            const total = 0.4 * item.text_score + 0.2 * item.pagerank + 0.15 * stScore + 0.1 * 1.0 + 0.1 * 0.8 + 0.05 * 1.0;
            return {
              id: item.id,
              score: total,
              components: [item.pagerank, stScore, 1.0, 0.8, 1.0],
            };
          }));
        }
        free() {
          freedCount++;
        }
      }

      const savedGlobalScorer = globalThis.HybridScorer;
      try {
        globalThis.HybridScorer = undefined;
        wasmLoader.__setHybridWasmCtorForTest(MockHybridWasmScorer);
        const wasmRows = wasmLoader.scoreBatchHybrid(batchInput, Presets.default);
        const payloadById = new Map((capturedPayload || []).map(p => [p.id, p]));
        assertEq(payloadById.get('wasm-open-ctrl')?.status, 0, 'WASM payload code for non-ephemeral open');
        assertEq(payloadById.get('wasm-eph-bool')?.status, 7, 'WASM payload code 7 (deferred) for open ephemeral:true');
        assertEq(payloadById.get('wasm-eph-inprog')?.status, 7, 'WASM payload code 7 (deferred) for in_progress ephemeral:1');
        assertEq(payloadById.get('wasm-closed-eph')?.status, 3, 'WASM payload code 3 (closed) for closed ephemeral');
        assertEq(payloadById.get('wasm-tomb-eph')?.status, 9, 'WASM payload code 9 (tombstone) for tombstone ephemeral');
        assertEq(freedCount, 1, 'WASM scorer instance freed after scoreBatchHybrid');

        const wasmById = new Map(wasmRows.map(r => [r.id, r]));
        assertClose(wasmById.get('wasm-open-ctrl').component_scores.status, 1.0, 1e-9, 'WASM path open control');
        assertClose(wasmById.get('wasm-eph-bool').component_scores.status, 0.2, 1e-9, 'WASM path ephemeral:true');
        assertClose(wasmById.get('wasm-eph-inprog').component_scores.status, 0.2, 1e-9, 'WASM path in_progress ephemeral:1');
        assertClose(wasmById.get('wasm-closed-eph').component_scores.status, 0.1, 1e-9, 'WASM path closed ephemeral control');
        assertClose(wasmById.get('wasm-tomb-eph').component_scores.status, 0.0, 1e-9, 'WASM path tombstone ephemeral control');
      } finally {
        wasmLoader.__setHybridWasmCtorForTest(null);
        globalThis.HybridScorer = savedGlobalScorer;
      }

      // Also verify JS fallback path when HybridWasmCtor is null
      const batchRows = wasmLoader.scoreBatchHybrid([
        { ...base, id: 'batch-open', status: 'open', ephemeral: 0 },
        { ...base, id: 'batch-eph-bool', status: 'open', ephemeral: true },
        { ...base, id: 'batch-eph-int', status: 'open', ephemeral: 1 },
        { ...base, id: 'batch-closed-eph', status: 'closed', ephemeral: 1 },
      ], Presets.default);
      const batchById = new Map(batchRows.map(r => [r.id, r]));
      assertClose(batchById.get('batch-open').component_scores.status, 1.0, 1e-9, 'scoreBatchHybrid fallback open control');
      assertClose(batchById.get('batch-eph-bool').component_scores.status, 0.2, 1e-9, 'scoreBatchHybrid fallback ephemeral:true');
      assertClose(batchById.get('batch-eph-int').component_scores.status, 0.2, 1e-9, 'scoreBatchHybrid fallback ephemeral:1');
      assertClose(batchById.get('batch-closed-eph').component_scores.status, 0.1, 1e-9, 'scoreBatchHybrid fallback closed ephemeral control');
    }
  }

  function verifyActivePageRankClamping() {
    const {
      computeActivePageRankBounds: prodBounds,
      resolveHybridPageRankMax: prodResolveMax,
      normalizeHybridPageRank: prodNormalizePR,
    } = scorerHelpers;
    if (typeof prodBounds !== 'function' || typeof prodResolveMax !== 'function' || typeof prodNormalizePR !== 'function') {
      throw new Error('Production PageRank normalization helpers not exported from hybrid_scorer.js');
    }

    const scorer = new ScorerClass(Presets['impact-first']);
    const nowIso = new Date().toISOString();

    const rawCorpus = [
      { id: 'active-open-top', textScore: 0.8, rawPR: 0.08, status: 'open', priority: 1, blockerCount: 1, updatedAt: nowIso },
      { id: 'active-inprog-mid', textScore: 0.8, rawPR: 0.02, status: 'in_progress', priority: 1, blockerCount: 1, updatedAt: nowIso },
      { id: 'closed-mega-hub', textScore: 0.8, rawPR: 0.80, status: 'closed', priority: 1, blockerCount: 0, updatedAt: nowIso },
      { id: 'tombstone-mega-hub', textScore: 0.8, rawPR: 0.95, status: 'tombstone', priority: 1, blockerCount: 0, updatedAt: nowIso },
    ];

    const bounds = prodBounds(rawCorpus.map(r => ({ pagerank: r.rawPR, status: r.status })));
    if (!bounds.hasMetrics) {
      throw new Error('expected hasMetrics=true');
    }
    assertClose(bounds.maxActivePageRank, 0.08, 1e-9, 'maxActivePageRank ignores closed and tombstone rows');
    assertClose(bounds.maxAnyPageRank, 0.95, 1e-9, 'maxAnyPageRank tracks overall corpus max');
    assertClose(bounds.maxPageRank, 0.08, 1e-9, 'maxPageRank prefers maxActivePageRank when > 0');

    // Edge cases on production helpers: all-closed fallback, maxPageRank === 0, and hasMetrics === false
    assertClose(prodResolveMax(0, 0.42), 0.42, 1e-9, 'resolveHybridPageRankMax falls back to maxAny when maxActive is 0');
    assertClose(prodResolveMax(0, 0), 0, 1e-9, 'resolveHybridPageRankMax returns 0 when all scores are 0');
    assertClose(prodNormalizePR(0.05, true, 0), 0, 1e-9, 'normalizeHybridPageRank returns 0 when maxPageRank === 0');
    assertClose(prodNormalizePR(0.05, false, 0), 0.5, 1e-9, 'normalizeHybridPageRank returns neutral 0.5 when hasMetrics === false');
    assertClose(prodNormalizePR(-0.2, true, 0.08), 0, 1e-9, 'normalizeHybridPageRank clamps negative raw score to 0');

    const candidates = rawCorpus.map(r => ({
      id: r.id,
      textScore: r.textScore,
      pagerank: prodNormalizePR(r.rawPR, bounds.hasMetrics, bounds.maxPageRank),
      status: r.status,
      priority: r.priority,
      blockerCount: r.blockerCount,
      updatedAt: r.updatedAt,
    }));

    const ranked = scorer.scoreAndRank(candidates);
    const byId = new Map(ranked.map(r => [r.id, r]));
    assertClose(byId.get('active-open-top').component_scores.pagerank, 1.0, 1e-9, 'active-open-top normalized PR');
    assertClose(byId.get('active-inprog-mid').component_scores.pagerank, 0.25, 1e-9, 'active-inprog-mid normalized PR');
    assertClose(byId.get('closed-mega-hub').component_scores.pagerank, 1.0, 1e-9, 'closed-mega-hub clamped PR <= 1.0');
    assertClose(byId.get('tombstone-mega-hub').component_scores.pagerank, 1.0, 1e-9, 'tombstone-mega-hub clamped PR <= 1.0');
    if (ranked[0].id !== 'active-open-top') {
      throw new Error(`expected active-open-top to rank #1 over closed/tombstone mega-hubs, got ${ranked[0].id}`);
    }
  }

  async function verifyScoreBatchHybridPageRankClampingParity() {
    if (!wasmLoader || typeof wasmLoader.scoreBatchHybrid !== 'function') {
      throw new Error('wasm_loader.js scoreBatchHybrid unavailable');
    }
    const nowIso = new Date().toISOString();
    const clampRows = [
      { id: 'pr-over', textScore: 0.8, pagerank: 2.5, status: 'open', priority: 1, blockerCount: 2, updatedAt: nowIso },
      { id: 'pr-unit', textScore: 0.8, pagerank: 1.0, status: 'open', priority: 1, blockerCount: 2, updatedAt: nowIso },
      { id: 'pr-mid', textScore: 0.8, pagerank: 0.4, status: 'open', priority: 1, blockerCount: 2, updatedAt: nowIso },
      { id: 'pr-nan', textScore: 0.8, pagerank: Number.NaN, status: 'open', priority: 1, blockerCount: 2, updatedAt: nowIso },
      { id: 'pr-neg', textScore: 0.8, pagerank: -0.5, status: 'open', priority: 1, blockerCount: 2, updatedAt: nowIso },
      { id: 'pr-zero', textScore: 0.8, pagerank: 0.0, status: 'open', priority: 1, blockerCount: 2, updatedAt: nowIso },
    ];

    const jsScorer = new ScorerClass(Presets.default);
    const jsRanked = jsScorer.scoreAndRank(clampRows);
    const jsById = new Map(jsRanked.map(r => [r.id, r]));

    assertClose(jsById.get('pr-over').component_scores.pagerank, 1.0, 1e-9, 'JS HybridScorer clamps pagerank 2.5 -> 1.0');
    assertClose(jsById.get('pr-neg').component_scores.pagerank, 0.0, 1e-9, 'JS HybridScorer clamps pagerank -0.5 -> 0.0');
    assertClose(jsById.get('pr-nan').component_scores.pagerank, 0.5, 1e-9, 'JS HybridScorer defaults NaN -> 0.5');
    assertClose(jsById.get('pr-mid').component_scores.pagerank, 0.4, 1e-9, 'JS HybridScorer preserves in-range 0.4');
    assertClose(jsById.get('pr-over').hybrid_score, jsById.get('pr-unit').hybrid_score, 1e-9, 'JS HybridScorer 2.5 matches 1.0 score');
    assertClose(jsById.get('pr-neg').hybrid_score, jsById.get('pr-zero').hybrid_score, 1e-9, 'JS HybridScorer -0.5 matches 0.0 score');

    // 1. Verify scoreBatchHybrid fallback parity vs HybridScorer
    const fallbackRanked = wasmLoader.scoreBatchHybrid(clampRows, Presets.default);
    const fbById = new Map(fallbackRanked.map(r => [r.id, r]));
    for (const row of clampRows) {
      assertClose(
        fbById.get(row.id).component_scores.pagerank,
        jsById.get(row.id).component_scores.pagerank,
        1e-9,
        `scoreBatchHybrid fallback pagerank component parity for ${row.id}`
      );
      assertClose(
        fbById.get(row.id).hybrid_score,
        jsById.get(row.id).hybrid_score,
        1e-9,
        `scoreBatchHybrid fallback hybrid_score parity for ${row.id}`
      );
    }

    // 2. Verify wasm_loader.js:scoreBatchHybrid non-fallback WASM payload clamping (with JS fallback disabled)
    let capturedPayload = null;
    class PassthroughWasmScorer {
      constructor(_cfgJson) {}
      score_batch(payloadJson) {
        capturedPayload = JSON.parse(payloadJson);
        return JSON.stringify(capturedPayload.map(item => ({
          id: item.id,
          score: 0.4 * item.text_score + 0.2 * item.pagerank + 0.15 * 1.0 + 0.1 * 1.0 + 0.1 * 0.8 + 0.05 * 1.0,
          components: [item.pagerank, 1.0, 1.0, 0.8, 1.0],
        })));
      }
      free() {}
    }

    const savedGlobalScorer = globalThis.HybridScorer;
    try {
      globalThis.HybridScorer = undefined;
      wasmLoader.__setHybridWasmCtorForTest(PassthroughWasmScorer);
      const wasmLoaderRows = wasmLoader.scoreBatchHybrid(clampRows, Presets.default);
      const payloadById = new Map((capturedPayload || []).map(p => [p.id, p]));
      assertClose(payloadById.get('pr-over').pagerank, 1.0, 1e-9, 'wasm_loader clamps payload pagerank 2.5 -> 1.0');
      assertClose(payloadById.get('pr-neg').pagerank, 0.0, 1e-9, 'wasm_loader clamps payload pagerank -0.5 -> 0.0');
      assertClose(payloadById.get('pr-nan').pagerank, 0.5, 1e-9, 'wasm_loader defaults payload NaN -> 0.5');
      assertClose(payloadById.get('pr-mid').pagerank, 0.4, 1e-9, 'wasm_loader preserves payload 0.4');

      const wlById = new Map(wasmLoaderRows.map(r => [r.id, r]));
      for (const row of clampRows) {
        assertClose(
          wlById.get(row.id).component_scores.pagerank,
          jsById.get(row.id).component_scores.pagerank,
          1e-9,
          `wasm_loader non-fallback pagerank parity vs HybridScorer for ${row.id}`
        );
        assertClose(
          wlById.get(row.id).hybrid_score,
          jsById.get(row.id).hybrid_score,
          1e-6,
          `wasm_loader non-fallback hybrid_score parity vs HybridScorer for ${row.id}`
        );
      }
    } finally {
      wasmLoader.__setHybridWasmCtorForTest(null);
      globalThis.HybridScorer = savedGlobalScorer;
    }

    // 3. When compiled bv_hybrid_scorer_bg.wasm is available, verify real Rust WASM binary clamping & end-to-end parity
    if (typeof require === 'function') {
      const fs = require('node:fs');
      const path = require('node:path');
      const { pathToFileURL } = require('node:url');
      const wasmDir = process.env.BV_HYBRID_WASM_DIR || path.resolve(__dirname, '..', '..', '..', '.bv', 'wasm', 'hybrid');
      const wasmJsPath = path.join(wasmDir, 'bv_hybrid_scorer.js');
      const wasmBgPath = path.join(wasmDir, 'bv_hybrid_scorer_bg.wasm');
      if (fs.existsSync(wasmJsPath) && fs.existsSync(wasmBgPath)) {
        const wasmMod = await import(pathToFileURL(wasmJsPath).href);
        wasmMod.initSync({ module: fs.readFileSync(wasmBgPath) });
        const RealWasmScorer = wasmMod.HybridScorer;

        // Direct Rust WASM check (unclamped raw JSON input -> Rust normalize_pagerank clamps to [0.0, 1.0])
        const rawDirectScorer = new RealWasmScorer(JSON.stringify(Presets.default));
        try {
          const rawDirectParsed = JSON.parse(rawDirectScorer.score_batch(JSON.stringify([
            { id: 'raw-over', text_score: 0.8, pagerank: 2.5, status: 0, priority: 1, blocker_count: 2, days_since_update: 0.0 },
            { id: 'raw-neg', text_score: 0.8, pagerank: -0.5, status: 0, priority: 1, blocker_count: 2, days_since_update: 0.0 },
            { id: 'raw-mid', text_score: 0.8, pagerank: 0.4, status: 0, priority: 1, blocker_count: 2, days_since_update: 0.0 },
          ])));
          const rawDirectById = new Map(rawDirectParsed.map(r => [r.id, r]));
          assertClose(rawDirectById.get('raw-over').components[0], 1.0, 1e-9, 'real Rust WASM clamps pagerank 2.5 -> 1.0');
          assertClose(rawDirectById.get('raw-neg').components[0], 0.0, 1e-9, 'real Rust WASM clamps pagerank -0.5 -> 0.0');
          assertClose(rawDirectById.get('raw-mid').components[0], 0.4, 1e-9, 'real Rust WASM preserves pagerank 0.4');
        } finally {
          rawDirectScorer.free();
        }

        // End-to-end scoreBatchHybrid + real Rust WASM vs JS HybridScorer
        try {
          globalThis.HybridScorer = undefined;
          wasmLoader.__setHybridWasmCtorForTest(RealWasmScorer);
          const realWasmRanked = wasmLoader.scoreBatchHybrid(clampRows, Presets.default);
          const realById = new Map(realWasmRanked.map(r => [r.id, r]));
          for (const row of clampRows) {
            assertClose(
              realById.get(row.id).component_scores.pagerank,
              jsById.get(row.id).component_scores.pagerank,
              1e-9,
              `real WASM scoreBatchHybrid pagerank parity vs HybridScorer for ${row.id}`
            );
            assertClose(
              realById.get(row.id).hybrid_score,
              jsById.get(row.id).hybrid_score,
              1e-5,
              `real WASM scoreBatchHybrid hybrid_score parity vs HybridScorer for ${row.id}`
            );
          }
        } finally {
          wasmLoader.__setHybridWasmCtorForTest(null);
          globalThis.HybridScorer = savedGlobalScorer;
        }
      }
    }
  }

  if (typeof require === 'function') {
    const test = require('node:test');
    test('HybridScorer baseline scoreAndRank', () => {
      verifyBaselineScore();
    });
    test('HybridScorer and scoreBatchHybrid downgrade open ephemeral issues to 0.2 with open 1.0 and closed 0.1 controls', () => {
      verifyEphemeralParityAndControls();
    });
    test('HybridScorer active PageRank normalization preserves active spread and clamps closed hubs to 1.0', () => {
      verifyActivePageRankClamping();
    });
    test('scoreBatchHybrid and HybridScorer clamp out-of-range PageRank [0.0, 1.0] with exact parity', async () => {
      await verifyScoreBatchHybridPageRankClampingParity();
    });
  } else {
    verifyBaselineScore();
    verifyEphemeralParityAndControls();
    verifyActivePageRankClamping();
    verifyScoreBatchHybridPageRankClampingParity();
    console.log('HybridScorer tests passed');
  }
})();
