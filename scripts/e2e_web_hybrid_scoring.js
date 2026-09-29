#!/usr/bin/env node

const { execFileSync } = require('child_process');
const fs = require('fs');
const path = require('path');
const vm = require('vm');

function log(msg) {
  const ts = new Date().toISOString();
  process.stdout.write(`[${ts}] ${msg}\n`);
}

function runBv(args, env) {
  const out = execFileSync('bv', args, { env, encoding: 'utf8' });
  return JSON.parse(out);
}

function parseJsonl(filePath) {
  const lines = fs.readFileSync(filePath, 'utf8').split(/\r?\n/).filter(Boolean);
  return lines.map((line, idx) => {
    try {
      return JSON.parse(line);
    } catch (err) {
      throw new Error(`Invalid JSONL at line ${idx + 1}: ${err.message}`);
    }
  });
}

function computeBlockerCounts(issues) {
  const counts = new Map();
  for (const issue of issues) {
    counts.set(issue.id, 0);
  }
  for (const issue of issues) {
    const deps = Array.isArray(issue.dependencies) ? issue.dependencies : [];
    for (const dep of deps) {
      const type = dep.type || 'blocks';
      if (type !== 'blocks') continue;
      const dependsOn = dep.depends_on_id;
      if (counts.has(dependsOn)) {
        counts.set(dependsOn, counts.get(dependsOn) + 1);
      }
    }
  }
  return counts;
}

function loadBrowserScripts(viewerDir) {
  global.window = global;
  const hybridPath = path.join(viewerDir, 'hybrid_scorer.js');
  const wasmPath = path.join(viewerDir, 'wasm_loader.js');
  const hybridCode = fs.readFileSync(hybridPath, 'utf8');
  const wasmCode = fs.readFileSync(wasmPath, 'utf8');
  vm.runInThisContext(hybridCode, { filename: 'hybrid_scorer.js' });
  vm.runInThisContext(wasmCode, { filename: 'wasm_loader.js' });
}

function topIds(results, n = 5) {
  return results.slice(0, n).map(r => r.issue_id || r.id).join(',');
}

async function main() {
  const root = path.resolve(__dirname, '..');
  const fixture = path.join(root, 'tests', 'testdata', 'search_hybrid.jsonl');
  const tmpDir = fs.mkdtempSync(path.join(require('os').tmpdir(), 'bv-web-hybrid-'));
  const beadsDir = path.join(tmpDir, '.beads');
  fs.mkdirSync(beadsDir, { recursive: true });
  fs.copyFileSync(fixture, path.join(beadsDir, 'beads.jsonl'));

  const env = {
    ...process.env,
    BEADS_DIR: beadsDir,
    BV_SEMANTIC_EMBEDDER: 'hash',
    BV_SEMANTIC_DIM: '384',
    BV_INSIGHTS_MAP_LIMIT: '0',
  };

  const issues = parseJsonl(fixture);
  const blockerCounts = computeBlockerCounts(issues);

  log(`Fixture issues: ${issues.length}`);
  log(`BEADS_DIR=${beadsDir}`);

  const insights = runBv(['--robot-insights'], env);
  const pagerank = insights?.full_stats?.pagerank || {};

  const text = runBv(['--search', 'auth', '--search-mode', 'text', '--search-limit', String(issues.length), '--robot-search'], env);
  const hybrid = runBv(['--search', 'auth', '--search-mode', 'hybrid', '--search-preset', 'impact-first', '--search-limit', String(issues.length), '--robot-search'], env);

  log(`Text top IDs: ${topIds(text.results)}`);
  log(`Hybrid top IDs: ${topIds(hybrid.results)}`);

  const issueMap = new Map(issues.map(i => [i.id, i]));
  const scoringInput = text.results.map(result => {
    const issue = issueMap.get(result.issue_id);
    return {
      id: result.issue_id,
      textScore: result.score,
      pagerank: pagerank[result.issue_id] ?? 0.5,
      status: issue?.status || 'open',
      priority: issue?.priority ?? 2,
      blockerCount: blockerCounts.get(result.issue_id) || 0,
      updatedAt: issue?.updated_at || issue?.created_at || null,
    };
  });

  loadBrowserScripts(path.join(root, 'pkg', 'export', 'viewer_assets'));

  if (typeof window.initHybridWasmScorer === 'function') {
    await window.initHybridWasmScorer(issues.length);
    if (typeof window.getHybridWasmStatus === 'function') {
      const status = window.getHybridWasmStatus();
      log(`Hybrid WASM status: ready=${status.ready} reason=${status.reason || 'none'}`);
    }
  }

  const weights = window.HYBRID_PRESETS['impact-first'];
  const jsRanked = window.scoreBatchHybrid(scoringInput, weights);
  log(`JS hybrid top IDs: ${jsRanked.slice(0, 5).map(r => r.id).join(',')}`);

  const mismatches = [];
  for (let i = 0; i < Math.min(hybrid.results.length, jsRanked.length); i++) {
    if (hybrid.results[i].issue_id !== jsRanked[i].id) {
      mismatches.push({
        index: i,
        go: hybrid.results[i].issue_id,
        js: jsRanked[i].id,
      });
    }
  }

  if (mismatches.length > 0) {
    log(`⚠️  Order mismatches found: ${JSON.stringify(mismatches)}`);
  } else {
    log('✅ JS ordering matches CLI hybrid ordering (impact-first)');
  }

  fs.rmSync(tmpDir, { recursive: true, force: true });
}

main().catch(err => {
  console.error(err);
  process.exit(1);
});
