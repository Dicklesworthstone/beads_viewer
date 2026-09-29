/* eslint-disable no-console */
(function runHybridScorerTests() {
  if (typeof HybridScorer === 'undefined') {
    console.warn('HybridScorer not available; skipping tests.');
    return;
  }

  function assertClose(actual, expected, tolerance, message) {
    if (Math.abs(actual - expected) > tolerance) {
      throw new Error(`${message}: got ${actual}, want ${expected}`);
    }
  }

  const scorer = new HybridScorer(HYBRID_PRESETS.default);
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
  console.log('HybridScorer tests passed');
})();
