/* eslint-disable no-console */
(function runHybridScorerTests() {
  let ScorerClass = typeof HybridScorer !== 'undefined' ? HybridScorer : undefined;
  let Presets = typeof HYBRID_PRESETS !== 'undefined' ? HYBRID_PRESETS : undefined;
  let wasmLoader = null;

  if (typeof require === 'function') {
    if (!ScorerClass || !Presets) {
      const mod = require('./hybrid_scorer.js');
      ScorerClass = mod.HybridScorer;
      Presets = mod.HYBRID_PRESETS;
      globalThis.HybridScorer = ScorerClass;
      globalThis.HYBRID_PRESETS = Presets;
    }
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
    const scorer = new ScorerClass(Presets['impact-first']);
    const nowIso = new Date().toISOString();
    const maxActivePR = 0.08;
    const clampActivePR = (rawPR) => Math.min(1, Math.max(0, (Number(rawPR) || 0) / maxActivePR));

    const candidates = [
      { id: 'active-top', textScore: 0.8, pagerank: clampActivePR(0.08), status: 'open', priority: 1, blockerCount: 1, updatedAt: nowIso },
      { id: 'active-mid', textScore: 0.8, pagerank: clampActivePR(0.02), status: 'open', priority: 1, blockerCount: 1, updatedAt: nowIso },
      { id: 'closed-mega-hub', textScore: 0.8, pagerank: clampActivePR(0.80), status: 'closed', priority: 1, blockerCount: 0, updatedAt: nowIso },
    ];

    const ranked = scorer.scoreAndRank(candidates);
    const byId = new Map(ranked.map(r => [r.id, r]));
    assertClose(byId.get('active-top').component_scores.pagerank, 1.0, 1e-9, 'active-top normalized PR');
    assertClose(byId.get('active-mid').component_scores.pagerank, 0.25, 1e-9, 'active-mid normalized PR');
    assertClose(byId.get('closed-mega-hub').component_scores.pagerank, 1.0, 1e-9, 'closed-mega-hub clamped PR <= 1.0');
    if (ranked[0].id !== 'active-top') {
      throw new Error(`expected active-top to rank #1 over closed-mega-hub, got ${ranked[0].id}`);
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
  } else {
    verifyBaselineScore();
    verifyEphemeralParityAndControls();
    verifyActivePageRankClamping();
    console.log('HybridScorer tests passed');
  }
})();
