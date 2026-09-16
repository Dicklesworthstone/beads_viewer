// Explicit opt-in real Chromium journeys. No DOM substitutes or browser packages.
// Artifacts are consumed by the failing assertion/reviewer and retained externally.
// Usage: node tests/scripts/dashboard_browser_test.mjs CHROMIUM BUNDLE ARTIFACTS blocking-types
// Export a seven-task fixture: workflow-root is qa-review; workflow-{blocks,
// conditional,legacy,waits,related,closed} depend on it with types {blocks,
// conditional-blocks,"",waits-for,team-reference,waits-for}, respectively.
// The dependent statuses are open except workflow-closed, which is closed.
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import http from 'node:http';
import { spawn } from 'node:child_process';

const [browser, bundle, artifacts, mode = 'journeys', updatedBundle, projectBundle] = process.argv.slice(2);
assert.ok(browser && bundle && artifacts, 'browser, bundle, artifacts required');
assert.ok(['journeys', 'offline-only', 'blocking-types', 'what-if', 'hits', 'readiness', 'metric-visibility', 'suggestion-visibility', 'layout-seeds'].includes(mode), 'unknown browser test mode');
fs.mkdirSync(artifacts, { recursive: true });
const records = [];
let brokenAsset = '', changedAsset = '', workerRevision = 0, chrome, server, socket;
let activeBundle = bundle;
let layoutVariant = null;
const mime = { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css',
  '.json': 'application/json', '.wasm': 'application/wasm', '.svg': 'image/svg+xml' };
server = http.createServer((req, res) => {
  res.on('finish', () => records.push({ serverRequest: req.url, status: res.statusCode, userAgent: req.headers['user-agent'] }));
  const url = new URL(req.url, 'http://localhost');
  let name = decodeURIComponent(url.pathname);
  if (name.endsWith('/')) name += 'index.html';
  const file = path.resolve(activeBundle, '.' + name);
  res.setHeader('Cache-Control', 'no-store');
  res.setHeader('Cross-Origin-Opener-Policy', 'same-origin');
  res.setHeader('Cross-Origin-Embedder-Policy', 'require-corp');
  if (!file.startsWith(path.resolve(activeBundle) + path.sep) || name === brokenAsset || !fs.existsSync(file)) {
    res.writeHead(404); res.end('Required file unavailable'); return;
  }
  res.setHeader('Content-Type', mime[path.extname(file)] || 'application/octet-stream');
  let body = fs.readFileSync(file);
  if (name === '/data/graph_layout.json' && mode === 'layout-seeds' && layoutVariant) {
    if (layoutVariant === 'stalled-headers') return;
    if (layoutVariant === 'stalled-body') { res.write('{"positions":'); return; }
    if (layoutVariant === 'missing') { res.writeHead(404); res.end('missing optional layout'); return; }
    if (layoutVariant === 'malformed') { res.end('{'); return; }
    const layout = JSON.parse(body);
    if (layoutVariant === 'partial') delete layout.positions[Object.keys(layout.positions)[0]];
    if (layoutVariant === 'stale-edge') layout.links[0].reverse();
    if (layoutVariant === 'stale-node') {
      const id = Object.keys(layout.positions)[0];
      layout.positions['absent-from-database'] = layout.positions[id];
      delete layout.positions[id];
    }
    if (layoutVariant === 'invalid-coordinate') layout.positions[Object.keys(layout.positions)[0]][0] = '100';
    // A matching artifact with bogus metrics must never suppress real computation.
    for (const tuple of Object.values(layout.metrics)) tuple.fill(999);
    body = Buffer.from(JSON.stringify(layout));
  }
  if (name === changedAsset) body = Buffer.concat([body, Buffer.from('\n// changed after export\n')]);
  if (name === '/coi-serviceworker.js' && workerRevision) {
    body = Buffer.concat([body, Buffer.from(`\n// Browser update control ${workerRevision}\n`)]);
  }
  res.end(body);
});
await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
const port = server.address().port;
const origin = `http://127.0.0.1:${port}`;
const pending = new Map();
let sequence = 0;
const sessions = new Map();
const monitorErrors = [];
const delay = ms => new Promise(resolve => setTimeout(resolve, ms));
function send(method, params = {}, sessionId) {
  const id = ++sequence;
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => { pending.delete(id); reject(new Error(`CDP timeout: ${method}`)); }, 30000);
    pending.set(id, { resolve, reject, timer, method, sessionId });
    socket.send(JSON.stringify({ id, method, params, ...(sessionId ? { sessionId } : {}) }));
  });
}
async function evaluate(page, expression) {
  const r = await send('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true, userGesture: true }, page.session);
  if (r.exceptionDetails) throw new Error(r.exceptionDetails.exception?.description || r.exceptionDetails.text);
  return r.result.value;
}
async function waitFor(page, expression, label, ms = 25000) {
  const until = Date.now() + ms;
  let last;
  while (Date.now() < until) {
    try { if (await evaluate(page, expression)) return; } catch (err) { last = err.message; }
    await delay(100);
  }
  throw new Error(`Timed out: ${label}${last ? ': ' + last : ''}`);
}
const app = `Alpine.$data(document.querySelector('[x-data="beadsApp()"]'))`;
const visible = `e => !!(e.getClientRects().length && getComputedStyle(e).visibility !== 'hidden')`;
async function capture(page, label) {
  if (page.worker) return;
  try {
    fs.writeFileSync(path.join(artifacts, `${page.name}-${label}.html`), await evaluate(page, 'document.documentElement.outerHTML'));
    const state = await evaluate(page, `typeof Alpine === 'undefined' ? null : (() => {const a=${app}; return JSON.parse(JSON.stringify({view:a.view, searchQuery:a.searchQuery, searchMode:a.searchMode, searchPreset:a.searchPreset, searchBackend:a.searchBackend, issues:a.issues, selectedIssue:a.selectedIssue, filters:a.filters, error:a.error, globalError:a.globalError}));})()`);
    fs.writeFileSync(path.join(artifacts, `${page.name}-${label}.json`), JSON.stringify(state, null, 2));
    const shot = await send('Page.captureScreenshot', { format: 'png', captureBeyondViewport: false }, page.session);
    fs.writeFileSync(path.join(artifacts, `${page.name}-${label}.png`), Buffer.from(shot.data, 'base64'));
  } catch (err) { records.push({ captureError: err.message, page: page.name }); }
}
async function openPage(name, width = 1280, offline = false) {
  const { browserContextId } = await send('Target.createBrowserContext');
  await send('Browser.grantPermissions', { browserContextId, origin, permissions: ['clipboardReadWrite', 'clipboardSanitizedWrite'] });
  const { targetId } = await send('Target.createTarget', { url: 'about:blank', browserContextId });
  const { sessionId } = await send('Target.attachToTarget', { targetId, flatten: true });
  const page = { name, session: sessionId, target: targetId, context: browserContextId, errors: [], external: [] };
  sessions.set(sessionId, page);
  for (const domain of ['Runtime', 'Page', 'Network', 'Log', 'ServiceWorker']) await send(`${domain}.enable`, {}, sessionId);
  await send('Network.setCacheDisabled', { cacheDisabled: true }, sessionId);
  await send('Emulation.setDeviceMetricsOverride', { width, height: 900, deviceScaleFactor: 1, mobile: width === 360 }, sessionId);
  await send('Fetch.enable', { patterns: [{ urlPattern: '*' }] }, sessionId);
  await send('Target.setAutoAttach', { autoAttach: true, waitForDebuggerOnStart: true, flatten: true }, sessionId);
  if (offline) await setOffline(page, true);
  await send('Page.navigate', { url: origin + '/' }, sessionId);
  return page;
}
async function setOffline(page, offline) {
  // Workers are separate DevTools targets. Emulate offline on both the page
  // and every worker; otherwise a worker could silently repair a missing cache
  // entry over the network while its client appears offline.
  // Every context shares this test origin, so connectivity changes globally.
  for (const target of sessions.values()) target.offline = offline;
  records.push({ networkState: offline ? 'offline' : 'online', page: page.name });
  // Stop the actual server too: browser-process service-worker update checks
  // are not necessarily emitted by an attached page or worker target.
  if (offline && server.listening) {
    const closed = new Promise(resolve => server.close(resolve));
    server.closeAllConnections();
    await closed;
  } else if (!offline && !server.listening) {
    await new Promise(resolve => server.listen(port, '127.0.0.1', resolve));
  }
  assert.equal(server.listening, !offline, 'actual origin listener matches network availability');
  records.push({ originListening: server.listening, page: page.name });
  const targets = [...sessions.values()].filter(p => !p.detached);
  await Promise.all(targets.map(p => emulateRequests(p, offline)));
  await Promise.all(targets.filter(p => !p.worker).map(p => send('Network.overrideNetworkState', { offline, latency: 0, downloadThroughput: -1, uploadThroughput: -1 }, p.session)));
  assert.equal(await evaluate(page, 'navigator.onLine'), !offline, 'browser exposes the actual emulated network state');
}
function emulateRequests(page, offline) {
  // Keep request emulation separate from navigator state. Applying the old
  // combined command to another worker can reset a reloaded page's onLine.
  return send('Network.emulateNetworkConditionsByRule', {
    offline, emulateOfflineServiceWorker: offline,
    matchedNetworkConditions: [{ urlPattern: '', offline, latency: 0, downloadThroughput: -1, uploadThroughput: -1 }],
  }, page.session);
}
async function ready(page) {
  await waitFor(page, `typeof Alpine !== 'undefined' && !${app}.loading && ${app}.stats.total === 4 && ${app}.graphReady`, 'four exported issues and actual graph WASM ready');
  assert.equal(await evaluate(page, `${app}.error || ${app}.globalError || null`), null);
}
async function reload(page) {
  await evaluate(page, 'window.__journeyBeforeReload = true');
  // A hard reload deliberately bypasses the service worker in Chromium.
  // Ordinary reload with the HTTP cache disabled tests the offline contract.
  await send('Page.reload', { ignoreCache: false }, page.session);
  await waitFor(page, '!window.__journeyBeforeReload', 'new document after reload');
  await ready(page);
  // Chrome's process-wide navigator override can reset on navigation. The
  // stopped origin plus worker request blocking is the offline proof.
  records.push({ reloadedOnline: await evaluate(page, 'navigator.onLine'), page: page.name, originListening: server.listening });
  if (page.offline) assert.equal(server.listening, false, 'offline reload succeeded while the origin was stopped');
}
async function click(page, selector, text) {
  const predicate = text === undefined ? visible : `e => (${visible})(e) && e.textContent.trim() === ${JSON.stringify(text)}`;
  await waitFor(page, `[...document.querySelectorAll(${JSON.stringify(selector)})].some(${predicate})`, `visible ${selector} ${text || ''}`);
  const point = await evaluate(page, `(() => { const e = [...document.querySelectorAll(${JSON.stringify(selector)})].find(${predicate}); e.scrollIntoView({block:'center'}); const r=e.getBoundingClientRect(); return {x:r.x+r.width/2,y:r.y+r.height/2}; })()`);
  assert.ok(point.x > 0 && point.x < await evaluate(page, 'innerWidth'), `${selector} is horizontally reachable`);
  await waitFor(page, `(() => { const e=[...document.querySelectorAll(${JSON.stringify(selector)})].find(${predicate}); return e?.contains(document.elementFromPoint(${point.x},${point.y})); })()`, `uncovered ${selector}`);
  for (const type of ['mousePressed', 'mouseReleased']) await send('Input.dispatchMouseEvent', { type, ...point, button: 'left', clickCount: 1 }, page.session);
}
async function key(page, key, code = key) {
  for (const type of ['keyDown', 'keyUp']) await send('Input.dispatchKeyEvent', { type, key, code, windowsVirtualKeyCode: key === 'Enter' ? 13 : key === 'Escape' ? 27 : 0 }, page.session);
}
async function search(page, text) {
  await click(page, 'input[placeholder="Search issues..."]');
  await evaluate(page, `(() => { const e=[...document.querySelectorAll('input[placeholder="Search issues..."]')].find(${visible}); e.value=${JSON.stringify(text)}; e.dispatchEvent(new Event('input',{bubbles:true})); })()`);
  await waitFor(page, `${app}.searchQuery === ${JSON.stringify(text)} && ${app}.view === 'issues'`, 'search applied');
  await delay(500);
}
function clean(page) {
  assert.deepEqual(monitorErrors, [], 'browser/worker network monitor configured');
  assert.deepEqual(page.external, [], `${page.name}: external network attempted`);
  assert.deepEqual(page.errors, [], `${page.name}: uncaught exception/CSP refusal`);
}
async function resultIDs(page, expected) {
  await waitFor(page, `JSON.stringify([...document.querySelectorAll('[aria-label^="View issue "]')].filter(${visible}).map(e => e.getAttribute('aria-label').split(':')[0].slice(11)).sort()) === ${JSON.stringify(JSON.stringify([...expected].sort()))}`, `visible issue IDs ${expected}`);
}

async function layoutSeedsJourney(page, edgeless = false) {
  await ready(page);
  await waitFor(page, '!!navigator.serviceWorker.controller', 'layout worker controls page');
  await delay(500);
  await ready(page);
  // Exercise each optional artifact response against the same real SQLite/WASM
  // source. Bypass the worker cache so stale/corrupt origin responses reach fetch.
  await send('Network.setBypassServiceWorker', { bypass: true }, page.session);
  await evaluate(page, `(() => {window.__layoutHovered=null;document.addEventListener('bv-graph:nodeHover',e=>{window.__layoutHovered=e.detail?.node?.id || null;});})()`);
  const exported = JSON.parse(fs.readFileSync(path.join(activeBundle, 'data/graph_layout.json'), 'utf8'));
  const expected = exported.positions;
  if (edgeless) {
    assert.deepEqual(exported.links, [], 'edgeless exporter emits an empty array');
    assert.equal(exported.edge_count, 0);
    assert.equal(await evaluate(page, 'getGraphViewData().dependencies.length'), 0, 'actual SQLite graph is edgeless');
    assert.equal(await evaluate(page, 'GRAPH_STATE.graph.edgeCount()'), 0, 'actual WASM graph is edgeless');
  }
  const variants = edgeless ? ['valid', 'missing', 'valid']
    : ['valid', 'missing', 'malformed', 'partial', 'stale-edge', 'stale-node', 'invalid-coordinate', 'stalled-headers', 'stalled-body', 'valid'];
  for (const variant of variants) {
    layoutVariant = variant;
    await evaluate(page, `(() => {
      window.__layoutLoaded = null;
      document.addEventListener('bv-graph:dataLoaded', e => {
        const m=${app}.forceGraphModule;
        window.__layoutLoaded = {precomputed:e.detail.precomputed,
          nodes:m.getGraph().graphData().nodes.map(n=>({id:n.id,x:n.x,y:n.y,fx:n.fx,fy:n.fy,pagerank:n.pagerank})),
          metrics:Object.fromEntries(Object.entries(m.getMetrics()).map(([k,v])=>[k,v !== null && v !== undefined]))};
      }, {once:true});
    })()`);
    if (await evaluate(page, `${app}.view !== 'graph'`)) await click(page, 'a[href="#/graph"]');
    else await evaluate(page, `${app}.initForceGraphView()`);
    await waitFor(page, '!!window.__layoutLoaded', `${variant}: actual viewer graph load`);
    const initial = await evaluate(page, 'window.__layoutLoaded');
    assert.equal(initial.precomputed, variant === 'valid', `${variant}: accepted only matching layout`);
    assert.equal(initial.nodes.length, 4);
    for (const n of initial.nodes) {
      assert.equal(n.fx, null, `${variant}: ${n.id} free on x`);
      assert.equal(n.fy, null, `${variant}: ${n.id} free on y`);
      assert.ok(Number.isFinite(n.pagerank) && n.pagerank !== 999, 'actual browser metric, not exported sentinel');
      if (variant === 'valid') assert.deepEqual([n.x,n.y], expected[n.id], `${n.id}: exact exported seed at dataLoaded`);
    }
    for (const metric of ['pagerank','betweenness','criticalPath','eigenvector','kcore','cycles']) {
      assert.equal(initial.metrics[metric], true, `${metric} remains computed`);
    }
    records.push({ layoutVariant:variant, initial });
    await waitFor(page, `!${app}.forceGraphLoading`, 'viewer finishes graph initialization');
    await waitFor(page, `${app}.graphLoadingStage === null`, 'simulation loading overlay completes');
    await delay(400); // Let Alpine's 300ms leave transition finish before the next reload.
  }
  await delay(1000);
  assert.ok(await evaluate(page, `${app}.forceGraphModule.getGraph().graphData().nodes.some(n=>{const p=${JSON.stringify(expected)}[n.id];return n.x!==p[0] || n.y!==p[1];})`), 'live simulation moves seeded nodes');
  await evaluate(page, `${app}.forceGraphModule.setFilter('search','Orchid root')`);
  assert.deepEqual(await evaluate(page, `${app}.forceGraphModule.getGraph().graphData().nodes.map(n=>n.id)`), ['browser-root']);
  await evaluate(page, `${app}.forceGraphModule.setFilter('search','')`);
  await delay(1000);
  // Movement was verified above. End physics before targeting a real canvas
  // node so an isolated component cannot move between measurement and click.
  await evaluate(page, `${app}.forceGraphModule.getGraph().cooldownTicks(0)`);
  await delay(100);
  await evaluate(page, `${app}.forceGraphModule.getGraph().zoomToFit(0,50)`);
  await delay(100);
  const point = await evaluate(page, `(() => {const g=${app}.forceGraphModule.getGraph();const n=g.graphData().nodes.find(n=>n.id==='browser-root');const p=g.graph2ScreenCoords(n.x,n.y);const r=document.querySelector('#graph-container canvas').getBoundingClientRect();return {x:r.x+p.x,y:r.y+p.y};})()`);
  await evaluate(page, `(() => {window.__graphPointerEvents=[];for(const type of ['pointerdown','pointerup','click','bv-graph:nodeClick','bv-graph:backgroundClick']) document.addEventListener(type,e=>window.__graphPointerEvents.push({type,target:e.target.tagName,id:e.detail?.node?.id,x:e.clientX,y:e.clientY,buttons:e.buttons}),{capture:true});})()`);
  await send('Input.dispatchMouseEvent', {type:'mouseMoved',...point},page.session);
  // ForceGraph throttles its hit-test canvas; visible coordinates alone do
  // not prove the pointer target has caught up with the last zoom.
  await waitFor(page, `window.__layoutHovered === 'browser-root'`, 'actual graph hit-test recognizes the target node');
  await send('Input.dispatchMouseEvent', {type:'mousePressed',...point,button:'left',buttons:1,clickCount:1},page.session);
  await delay(50);
  await send('Input.dispatchMouseEvent', {type:'mouseReleased',...point,button:'left',buttons:0,clickCount:1},page.session);
  await delay(100);
  records.push({graphPointer:await evaluate(page, 'window.__graphPointerEvents'), point, page:page.name});
  await waitFor(page, `${app}.graphDetailNode?.id === 'browser-root'`, 'seeded graph pointer opens detail after filter');
  const fitsContainer = `(() => {const g=${app}.forceGraphModule.getGraph();const c=document.getElementById('graph-container');return g.width()===c.clientWidth && g.height()===c.clientHeight;})()`;
  await delay(400);
  await waitFor(page, fitsContainer, 'graph fits container with detail open');
  await capture(page, 'layout-seeds');
  await click(page, 'button[title="Close detail pane (Esc)"]');
  await waitFor(page, `!${app}.graphDetailNode`, 'close detail pane');
  await delay(300);
  await waitFor(page, fitsContainer, 'graph fits container after detail closes');
  clean(page);
  assert.ok(records.some(r=>r.serverRequest === '/data/graph_layout.json'), 'actual layout HTTP request');
  layoutVariant = null;
  console.log(`PASS: ${page.name} matching seeds, ${variants.length} layout cases, metrics, movement, filter and detail`);
}
async function blockingTypesJourney(page) {
  await waitFor(page, `typeof Alpine !== 'undefined' && !${app}.loading && ${app}.stats.total === 7 && ${app}.graphReady`, 'seven workflow issues and actual graph WASM');
  await waitFor(page, '!!navigator.serviceWorker.controller', 'workflow bundle worker controls page');
  await delay(500);
  await waitFor(page, `typeof Alpine !== 'undefined' && !${app}.loading && ${app}.graphReady`, 'workflow app after worker activation');
  const active = ['workflow-blocks', 'workflow-conditional', 'workflow-legacy', 'workflow-waits'];
  const all = [...active, 'workflow-closed'].sort();
  const overview = await evaluate(page, `execQuery("SELECT dependent_count, blocks_ids, status FROM issue_overview_mv WHERE id = 'workflow-root'")[0]`);
  assert.equal(overview.status, 'qa-review', 'custom workflow status is retained');
  assert.equal(overview.dependent_count, 4);
  assert.deepEqual(overview.blocks_ids.split(',').sort(), active, 'active ID list matches exported count');
  assert.deepEqual(await evaluate(page, '({blocked: getStats().blocked, actionable: getStats().actionable})'), { blocked: 4, actionable: 1 }, 'typed prerequisites keep dependents out of ready work');
  assert.deepEqual(await evaluate(page, 'getQuickWins().map(i=>i.id)'), ['workflow-related'], 'informational reference is the only ready issue');
  const closed = await evaluate(page, `execQuery("SELECT blocker_count, blocked_by_ids FROM issue_overview_mv WHERE id = 'workflow-closed'")[0]`);
  assert.equal(closed.blocker_count, 0);
  assert.ok(!closed.blocked_by_ids, 'closed endpoint has no active blockers');
  for (const id of active) {
    const row = await evaluate(page, `execQuery("SELECT blocker_count, blocked_by_ids FROM issue_overview_mv WHERE id = ?", [${JSON.stringify(id)}])[0]`);
    assert.equal(row.blocker_count, 1);
    assert.equal(row.blocked_by_ids, 'workflow-root');
    assert.deepEqual(await evaluate(page, `getIssueDependencies(${JSON.stringify(id)}).blockedBy.map(i=>i.id)`), ['workflow-root'], `${id}: correctly directed blocker lookup`);
  }
  assert.deepEqual(await evaluate(page, `getIssueDependencies('workflow-root').blocks.map(i=>i.id).sort()`), all, 'detail navigation retains historical relationships');
  assert.deepEqual(await evaluate(page, `getIssueDependencies('workflow-related')`), { blocks: [], blockedBy: [] }, 'custom informational relation remains nonblocking');
  const expected = [
    ['workflow-blocks', 'blocks'], ['workflow-closed', 'waits-for'],
    ['workflow-conditional', 'conditional-blocks'], ['workflow-legacy', ''], ['workflow-waits', 'waits-for'],
  ];
  assert.deepEqual(await evaluate(page, `getGraphViewData().dependencies.map(d=>[d.issue_id,d.type]).sort((a,b)=>a[0].localeCompare(b[0]))`), expected, 'graph query retains all blocking variants and original types');
  assert.deepEqual(await evaluate(page, '[GRAPH_STATE.graph.nodeCount(), GRAPH_STATE.graph.edgeCount()]'), [7, 5]);
  await send('Page.navigate', { url: origin + '/#/issue/workflow-conditional' }, page.session);
  await waitFor(page, `typeof Alpine !== 'undefined' && ${app}.selectedIssue?.id === 'workflow-conditional'`, 'conditional dependent details');
  await key(page, 'h', 'KeyH');
  await waitFor(page, `${app}.selectedIssue?.id === 'workflow-root'`, 'h navigates to prerequisite');
  await key(page, 'l', 'KeyL');
  await waitFor(page, `${app}.selectedIssue?.id === 'workflow-blocks'`, 'l navigates to first dependent');
  await capture(page, 'dependency-navigation');
  await key(page, 'Escape');
  await click(page, 'a[href="#/graph"]');
  await waitFor(page, `${app}.forceGraphReady && !${app}.forceGraphLoading && !!document.querySelector('#graph-container canvas')`, 'force graph draws blocking variants');
  assert.equal(await evaluate(page, `${app}.forceGraphModule.getWasmGraph().edgeCount()`), 5, 'force graph WASM uses same edges');
  assert.deepEqual(await evaluate(page, `${app}.forceGraphModule.getGraph().graphData().links.map(e=>[e.source.id,e.target.id,e.type]).sort((a,b)=>a[0].localeCompare(b[0]))`), expected.map(([id, type])=>[id, 'workflow-root', type || 'blocks']), 'rendered links preserve endpoint direction');
  await capture(page, 'blocking-graph');
  clean(page);
  console.log('PASS: real exported SQLite, blocking ID lists, issue h/l navigation, graph WASM and force-graph links');
}

// Export the readiness fixture with closed rows excluded: three ready tasks
// (open, in-progress, resolved prerequisites), a missing dependency, a deferred
// task, a child inheriting its parent's review gate, that parent, a qa-review
// prerequisite and an explicit blocked lifecycle. Closed/deleted prerequisites
// remain in the full source; missing is genuinely absent.
async function readinessJourney(page) {
  await waitFor(page, `typeof Alpine !== 'undefined' && !${app}.loading && ${app}.stats.total === 9 && ${app}.graphReady`, 'nine visible readiness issues and actual graph WASM');
  await waitFor(page, '!!navigator.serviceWorker.controller', 'readiness worker controls page');
  await delay(500);
  await waitFor(page, `typeof Alpine !== 'undefined' && !${app}.loading && ${app}.graphReady`, 'readiness app after worker activation');
  const readyIDs = ['ready-open', 'ready-progress', 'ready-resolved'];
  const blockedIDs = ['missing-child', 'parent', 'parent-child'];
  const stats = await evaluate(page, 'getStats()');
  console.log(`${page.name} readiness:`, JSON.stringify(stats));
  assert.equal(stats.actionable, 3, 'only three proven ready tasks, including in-progress and omitted resolved prerequisites');
  assert.equal(stats.blocked, 3, 'missing and inherited gates remain blocked');
  assert.equal(stats.active, 9, 'active count includes unresolved lifecycles exactly once');
  assert.equal(stats.closed, 0, 'absent lifecycle count is zero');
  assert.deepEqual(await evaluate(page, 'getQuickWins(20).map(i=>i.id).sort()'), readyIDs, 'quick wins exclude deferred, unknown, inherited and nonready lifecycle tasks');
  assert.deepEqual(await evaluate(page, 'getActionableIssues().sort()'), readyIDs, 'actionable lookup uses full-source readiness rather than synthetic graph roots');
  assert.ok((await evaluate(page, 'topWhatIf(100).map(i=>i.issueId)')).every(id=>readyIDs.includes(id)), 'cascade recommendations consider only proven ready issues');
  assert.deepEqual(await evaluate(page, 'queryIssues({hasBlockers:false}).map(i=>i.id).sort()'), readyIDs, 'Ready filter uses the same readiness policy');
  const readyCard = '[\\@click*="filters.hasBlockers = false"][\\@click*="view ="]';
  await click(page, readyCard);
  await resultIDs(page, readyIDs);
  await capture(page, 'ready-filter');
  await click(page, 'a[href="#/"]');
  await click(page, '[\\@click*="filters.hasBlockers = true"][\\@click*="view ="]');
  await resultIDs(page, blockedIDs);
  await capture(page, 'blocked-filter');
  await click(page, 'a[href="#/insights"]');
  await waitFor(page, `[...document.querySelectorAll('[x-text*="stats.active"]')].filter(${visible}).some(e=>e.textContent.trim()==='9')`, 'visible finite active-node count');
  await capture(page, 'active-count');
  clean(page);
  console.log(`PASS: ${page.name} full-source readiness, quick wins, card filters and active counts`);
}

// Reuse the asymmetric sim-* fixture below. Its leading dependent (hub) and
// prerequisite (authority) differ, so swapping the score vectors also fails.
async function hitsJourney(page) {
  await waitFor(page, `typeof Alpine !== 'undefined' && !${app}.loading && ${app}.stats.total === 6 && ${app}.graphReady`, 'six visible issues and actual HITS WASM');
  await waitFor(page, '!!navigator.serviceWorker.controller', 'HITS bundle worker controls page');
  await delay(500);
  await waitFor(page, `typeof Alpine !== 'undefined' && !${app}.loading && ${app}.graphReady`, 'HITS app after worker activation');
  await click(page, 'a[href="#/insights"]');
  const raw = await evaluate(page, 'GRAPH_STATE.graph.hitsDefault()');
  assert.ok(raw.hubs.some(v => v > 0) && raw.authorities.some(v => v > 0), 'real engine computed both score vectors');
  const before = await evaluate(page, '[GRAPH_STATE.graph.nodeCount(), GRAPH_STATE.graph.edgeCount()]');
  for (const [state, field, title, leader] of [
    ['topByHITSHub', 'hits_hub', 'HITS Hubs', 'sim-child'],
    ['topByHITSAuth', 'hits_auth', 'HITS Authorities', 'sim-root'],
  ]) {
    await click(page, 'a[href="#/insights"]');
    const rows = await evaluate(page, `JSON.parse(JSON.stringify(${app}.${state}.map(i=>({id:i.id,title:i.title,score:i.${field}}))))`);
    console.log(`${page.name} ${title}:`, JSON.stringify(rows));
    assert.equal(rows[0]?.id, leader, `${title}: correct directed leader appears in dashboard`);
    assert.equal(rows.length, 6, `${title}: all visible issues are ranked, excluding synthetic tombstone`);
    assert.ok(rows[0].score > 0, `${title}: positive leader score`);
    assert.ok(rows.every((r, i) => Number.isFinite(r.score) && r.score >= 0 && r.score <= 1 && (i === 0 || rows[i-1].score >= r.score)), `${title}: finite normalized scores in descending order`);
    const panel = `[...document.querySelectorAll('.metric-panel-premium')].find(e=>e.querySelector('h3')?.textContent.trim()===${JSON.stringify(title)})`;
    await waitFor(page, `${panel} && [...${panel}.querySelectorAll('.metric-item')].filter(${visible}).length === 5`, `${title}: visible rendered ranking cards`);
    const rendered = await evaluate(page, `[...${panel}.querySelectorAll('.metric-item')].filter(${visible}).map(e=>({title:e.querySelector('.metric-item-title').textContent,score:e.querySelector('.metric-item-score').textContent}))`);
    assert.deepEqual(rendered, rows.slice(0, 5).map(r=>({title:r.title,score:r.score.toFixed(3)})), `${title}: rendered scores and ordering match ranked issues`);
    assert.equal(await evaluate(page, `[...${panel}.querySelectorAll('p')].some(e=>(${visible})(e) && e.textContent.includes('No HITS'))`), false, `${title}: empty-data fallback hidden`);
    await click(page, `template[x-for*="${state}.slice"] + .metric-item`);
    await waitFor(page, `${app}.selectedIssue?.id === ${JSON.stringify(leader)}`, `${title}: card opens its actual issue`);
    await capture(page, `${field}-detail`);
    await key(page, 'Escape');
  }
  await click(page, 'a[href="#/insights"]');
  assert.deepEqual(await evaluate(page, '[GRAPH_STATE.graph.nodeCount(), GRAPH_STATE.graph.edgeCount()]'), before, 'ranking and navigation do not mutate graph');
  const slack = await evaluate(page, 'Array.from(GRAPH_STATE.graph.slack()).map((value,idx)=>({id:GRAPH_STATE.graph.nodeId(idx),value})).filter(row=>getIssue(row.id))');
  assert.ok(slack.some(row=>row.value > 0), 'asymmetric fixture includes flexible work');
  assert.deepEqual(await evaluate(page, 'getIssuesBySlack(100,true).map(i=>i.id).sort()'), slack.filter(row=>row.value===0).map(row=>row.id).sort(), 'zero-slack filter excludes flexible work');
  assert.deepEqual(await evaluate(page, 'getIssuesBySlack(100,false).map(i=>i.slack)'), slack.map(row=>row.value).sort((a,b)=>b-a), 'flexible-work list preserves all actual slack scores');
  await capture(page, 'hits-panels');
  clean(page);
  console.log(`PASS: ${page.name} actual HITS hub/authority rankings, rendered scores and issue navigation`);
}

// Six actual issues: rank-root and five rank-branch-N dependents. Every branch
// also depends on eleven missing-NN endpoints. Those endpoints remain in the
// graph, but must not crowd actual issues out of a limited ranking panel.
async function metricVisibilityJourney(page) {
  await waitFor(page, `typeof Alpine !== 'undefined' && !${app}.loading && ${app}.stats.total === 6 && ${app}.graphReady`, 'six visible issues and actual graph WASM');
  await waitFor(page, '!!navigator.serviceWorker.controller', 'ranking worker controls page');
  await delay(500);
  await waitFor(page, `typeof Alpine !== 'undefined' && !${app}.loading && ${app}.graphReady`, 'ranking app after worker activation');
  await click(page, 'a[href="#/insights"]');
  const before = await evaluate(page, '[GRAPH_STATE.graph.nodeCount(), GRAPH_STATE.graph.edgeCount()]');
  assert.deepEqual(before, [17, 60], 'missing endpoints participate in real graph analysis');
  const visibleIDs = ['rank-branch-1', 'rank-branch-2', 'rank-branch-3', 'rank-branch-4', 'rank-branch-5', 'rank-root'];
  const initial = await evaluate(page, `JSON.parse(JSON.stringify(${app}.topByHITSAuth.map(i=>({id:i.id,score:i.hits_auth}))))`);
  console.log(`${page.name} authority ranking:`, JSON.stringify(initial));
  assert.equal(initial.length, 6, 'authority ranking retains all six actual issues despite eleven missing endpoints');
  assert.equal(initial[0].id, 'rank-root', 'real prerequisite leads authorities');
  assert.ok(initial[0].score > 0, 'real prerequisite has positive authority');
  for (const [fn, field, vector, suffix] of [
    ['getTopByHITSAuth', 'hits_auth', 'GRAPH_STATE.graph.hitsDefault().authorities', ''],
    ['getTopByHITSHub', 'hits_hub', 'GRAPH_STATE.graph.hitsDefault().hubs', ''],
    ['getTopByKCore', 'kcore', 'GRAPH_STATE.graph.kcore()', ''],
    ['getTopByBetweenness', 'betweenness', 'GRAPH_STATE.graph.betweenness()', ''],
    ['getIssuesBySlack', 'slack', 'GRAPH_STATE.graph.slack()', ',true'],
    ['getIssuesBySlack', 'slack', 'GRAPH_STATE.graph.slack()', ',false'],
  ]) {
    const all = await evaluate(page, `${fn}(100${suffix}).map(i=>({id:i.id,score:i.${field}}))`);
    assert.deepEqual(all.map(i=>i.id).sort(), visibleIDs, `${fn}${suffix}: each actual issue appears once`);
    const raw = await evaluate(page, `Array.from(${vector}).map((score,idx)=>({id:GRAPH_STATE.graph.nodeId(idx),score}))`);
    for (const row of all) {
      assert.equal(row.score, raw.find(r=>r.id===row.id).score, `${fn}: preserves actual WASM score for ${row.id}`);
      assert.ok(Number.isFinite(row.score), `${fn}: finite score`);
    }
    assert.ok(all.every((r,i)=>i===0 || all[i-1].score>=r.score), `${fn}: descending order`);
    for (const limit of [0, 1, 3, 10]) {
      assert.deepEqual(await evaluate(page, `${fn}(${limit}${suffix}).map(i=>i.id)`), all.slice(0,limit).map(i=>i.id), `${fn}${suffix}: limit counts actual issue rows`);
    }
  }
  const panel = `[...document.querySelectorAll('.metric-panel-premium')].find(e=>e.querySelector('h3')?.textContent.trim()==='HITS Authorities')`;
  await waitFor(page, `${panel} && [...${panel}.querySelectorAll('.metric-item')].filter(${visible}).length === 5`, 'five rendered authority cards');
  const cards = await evaluate(page, `[...${panel}.querySelectorAll('.metric-item')].filter(${visible}).map(e=>e.querySelector('.metric-item-title').textContent)`);
  assert.deepEqual(cards, ['Real prerequisite', 'Visible branch 1', 'Visible branch 2', 'Visible branch 3', 'Visible branch 4']);
  await capture(page, 'authority-ranking');
  await click(page, 'template[x-for*="topByHITSAuth.slice"] + .metric-item');
  await waitFor(page, `${app}.selectedIssue?.id === 'rank-root'`, 'authority card opens actual issue');
  assert.deepEqual(await evaluate(page, '[GRAPH_STATE.graph.nodeCount(), GRAPH_STATE.graph.edgeCount()]'), before, 'ranking does not remove missing graph context');
  await capture(page, 'authority-detail');
  clean(page);
  console.log(`PASS: ${page.name} visible ranking limits, raw scores, rendered cards and issue navigation`);
}

// Six visible issues: root->child, three children of absent, and joint which
// requires both root and absent. Completing absent would fabricate root's gain.
async function suggestionVisibilityJourney(page) {
  await waitFor(page, `typeof Alpine !== 'undefined' && !${app}.loading && ${app}.stats.total === 6 && ${app}.graphReady`, 'six suggestion issues and actual WASM');
  await waitFor(page, '!!navigator.serviceWorker.controller', 'suggestion worker controls page');
  await delay(500);
  await waitFor(page, `typeof Alpine !== 'undefined' && !${app}.loading && ${app}.graphReady`, 'suggestion app after worker activation');
  await click(page, 'a[href="#/insights"]');
  const before = await evaluate(page, '[GRAPH_STATE.graph.nodeCount(), GRAPH_STATE.graph.edgeCount()]');
  assert.deepEqual(before, [7, 6], 'missing prerequisite stays in the graph');
  const initial = await evaluate(page, `JSON.parse(JSON.stringify(${app}.topKSet))`);
  console.log(`${page.name} priority picks:`, JSON.stringify(initial));
  assert.deepEqual(initial.items.map(i=>i.issueId), ['pick-root'], 'priority cards select only an actual issue before computing gains');
  for (const limit of [1, 5, 100]) {
    const picks = await evaluate(page, `getTopKSet(${limit})`);
    assert.deepEqual(picks.items.map(i=>[i.issueId,i.marginal_gain,i.unblocked_issue_ids]), [['pick-root',1,['pick-child']]], 'missing prerequisite neither selected nor presumed completed');
    assert.equal(picks.total_gain, 1);
    assert.equal(picks.open_nodes, 6, 'candidate count excludes the synthetic endpoint');
    const impact = await evaluate(page, `topWhatIf(${limit})`);
    assert.deepEqual(impact.map(i=>[i.issueId,i.result.transitive_unblocks]), [['pick-root',1]], 'top cascade limit is applied after candidate eligibility');
  }
  assert.deepEqual(await evaluate(page, 'getTopKSet(0).items'), []);
  assert.deepEqual(await evaluate(page, 'topWhatIf(0)'), []);
  assert.deepEqual(await evaluate(page, 'getActionableIssues()'), ['pick-root']);
  const raw = await evaluate(page, `(() => {const r=GRAPH_STATE.graph.topkSet(buildClosedSet(),5);return r.items.map(i=>[GRAPH_STATE.graph.nodeId(i.node),i.marginal_gain]);})()`);
  assert.deepEqual(raw, [['absent',3],['pick-root',2]], 'unconstrained engine still sees the full graph, exposing the negative control');
  assert.equal(await evaluate(page, `buildClosedSet()[GRAPH_STATE.nodeMap.get('absent')]`), 0, 'excluded prerequisite is still unresolved');
  for (const selector of ['template[x-for*="topKSet?.items"] + div', 'template[x-for*="topImpactIssues.slice"] + div']) {
    await click(page, selector);
    await waitFor(page, `${app}.selectedIssue?.id === 'pick-root'`, 'suggestion card opens its actual issue');
    await capture(page, 'suggestion-detail');
    await key(page, 'Escape');
    await click(page, 'a[href="#/insights"]');
  }
  assert.deepEqual(await evaluate(page, '[GRAPH_STATE.graph.nodeCount(), GRAPH_STATE.graph.edgeCount()]'), before, 'selection and navigation preserve graph topology');
  await capture(page, 'suggestion-cards');
  clean(page);
  console.log(`PASS: ${page.name} visible candidate selection, true gains, readiness and card navigation`);
}

// The seven-task fixture has root<-child<-leaf, child also depending on closed
// and tombstone prerequisites, held depending on root+other, and other related
// to root without blocking. All remaining statuses are open; IDs use sim-.
// An optional second bundle uses --pages-include-closed=false on the same source.
async function whatIfJourney(page, includeClosed = true) {
  await waitFor(page, `typeof Alpine !== 'undefined' && !${app}.loading && ${app}.stats.total === ${includeClosed ? 6 : 5} && ${app}.graphReady`, 'visible simulation issues (resolved rows filtered) and real graph WASM');
  await waitFor(page, '!!navigator.serviceWorker.controller', 'simulation worker controls page');
  await delay(500);
  await send('Page.navigate', { url: origin + '/#/issue/sim-root' }, page.session);
  await waitFor(page, `typeof Alpine !== 'undefined' && ${app}.selectedIssue?.id === 'sim-root'`, 'root issue details');
  await click(page, 'button', 'Simulate Close');
  await waitFor(page, `${app}.whatIfResult !== null`, 'actual simulation result');
  const result = await evaluate(page, `JSON.parse(JSON.stringify(${app}.whatIfResult))`);
  console.log('Observed root what-if:', JSON.stringify(result));
  assert.equal(result.direct_unblocks, 1, 'closing root directly releases child only');
  assert.equal(result.transitive_unblocks, 2, 'child completion then releases leaf');
  assert.deepEqual(result.cascade_issue_ids.sort(), ['sim-child', 'sim-leaf']);
  await waitFor(page, String.raw`[...document.querySelectorAll('p')].some(e=>(${visible})(e) && /Closing this issue would unblock 1 issue\(s\) and enable 2 downstream item\(s\)/.test(e.textContent.replace(/\s+/g,' ').trim()))`, 'visible direct and transitive counts');
  assert.equal(await evaluate(page, `whatIfClose('sim-leaf').direct_unblocks`), 0, 'leaf cannot release its prerequisites');
  assert.equal(await evaluate(page, `whatIfClose('sim-other').direct_unblocks`), 0, 'held still needs root');
  assert.equal(await evaluate(page, `whatIfClose('sim-deleted').transitive_unblocks`), 0, 'tombstone is already resolved');
  assert.equal(await evaluate(page, `whatIfClose('sim-closed').transitive_unblocks`), 0, 'closed prerequisite remains resolved even when omitted');
  assert.deepEqual(await evaluate(page, `[...getResolvedIssueIDs()].sort()`), ['sim-closed', 'sim-deleted']);
  assert.deepEqual(await evaluate(page, `getActionableIssues().sort()`), ['sim-other', 'sim-root']);
  assert.deepEqual(await evaluate(page, `getTopKSet(5).items.map(i=>[i.issueId,i.marginal_gain])`), [['sim-root', 2], ['sim-other', 1]]);
  await capture(page, 'what-if-detail');
  await key(page, 'Escape');
  await click(page, 'a[href="#/insights"]');
  await waitFor(page, `[...document.querySelectorAll('span')].some(e=>(${visible})(e) && e.textContent.trim()==='3 potential unblocks')`, 'visible combined priority gain');
  await waitFor(page, `[...document.querySelectorAll('[x-text="item.marginal_gain"]')].filter(${visible}).map(e=>e.textContent).join(',')==='2,1'`, 'visible per-pick gains');
  await waitFor(page, `[...document.querySelectorAll('[x-text="item.result?.transitive_unblocks || 0"]')].filter(${visible}).map(e=>e.textContent).join(',')==='2'`, 'visible cascade impact card');
  await capture(page, 'what-if-priorities');
  await click(page, 'a[href="#/graph"]');
  await waitFor(page, `${app}.forceGraphReady && !${app}.forceGraphLoading && !!document.querySelector('#graph-container canvas')`, 'actual force graph');
  const forceResult = await evaluate(page, `(()=>{const m=${app}.forceGraphModule; return m.performWhatIf(m.getGraph().graphData().nodes.find(n=>n.id==='sim-root'));})()`);
  assert.equal(forceResult.direct_unblocks, 1);
  assert.equal(forceResult.transitive_unblocks, 2);
  await waitFor(page, `${app}.forceGraphModule.getWhatIfState().unblockedCount === 1`, 'child animation executes');
  // The graph uses a custom canvas renderer, not ForceGraph's linkColor accessor.
  // Draw each real link with that renderer into a real browser canvas and inspect
  // its pixels; alpha/antialiasing can round the theme's RGB by a few units.
  const greenLinks = await evaluate(page, `(()=>{
    const g=${app}.forceGraphModule.getGraph(), draw=g.linkCanvasObject();
    return g.graphData().links.filter(link=>{
      const c=new OffscreenCanvas(256,256), ctx=c.getContext('2d');
      const a=link.source, b=link.target;
      const scale=180/Math.max(Math.abs(b.x-a.x),Math.abs(b.y-a.y),1);
      ctx.setTransform(scale,0,0,scale,128-scale*(a.x+b.x)/2,128-scale*(a.y+b.y)/2);
      draw(link,ctx,scale);
      const pixels=ctx.getImageData(0,0,256,256).data;
      for(let i=0;i<pixels.length;i+=4) {
        if(pixels[i+3]>100 && Math.abs(pixels[i]-80)<=3 && Math.abs(pixels[i+1]-250)<=3 && Math.abs(pixels[i+2]-123)<=3) return true;
      }
      return false;
    }).map(link=>[link.source.id,link.target.id]).sort();
  })()`);
  assert.deepEqual(greenLinks, includeClosed ? [['sim-child','sim-closed'],['sim-child','sim-root']] : [['sim-child','sim-root']], 'visible resolved prerequisite edges glow in dependent-to-prerequisite direction');
  await capture(page, 'what-if-graph');
  await evaluate(page, `(()=>{const m=${app}.forceGraphModule; m.resetWhatIf(); m.performWhatIf(m.getGraph().graphData().nodes.find(n=>n.id==='sim-root')); m.resetWhatIf();})()`);
  await delay(900);
  assert.deepEqual(await evaluate(page, `${app}.forceGraphModule.getWhatIfState()`), { active: false, sourceNode: null, unblockedCount: 0 }, 'cancelled cascade cannot revive itself');
  assert.equal(await evaluate(page, `${app}.forceGraphModule.getGraph().graphData().nodes.some(n=>n._whatIfState)`), false, 'reset leaves no stale node state');
  await evaluate(page, `(()=>{const m=${app}.forceGraphModule; m.resetCriticalPath(); m.resetCycleNavigator(); const config=m.getConfig(); m.setConfig('linkWidth', config.linkWidth);})()`);
  await evaluate(page, `(()=>{const m=${app}.forceGraphModule; m.performWhatIf(m.getGraph().graphData().nodes.find(n=>n.id==='sim-root')); const data=getGraphViewData(); m.loadData(data.issues,data.dependencies,null);})()`);
  await delay(900);
  assert.deepEqual(await evaluate(page, `${app}.forceGraphModule.getWhatIfState()`), { active: false, sourceNode: null, unblockedCount: 0 }, 'data reload cancels a pending cascade');
  await evaluate(page, `(()=>{const m=${app}.forceGraphModule; m.performWhatIf(m.getGraph().graphData().nodes.find(n=>n.id==='sim-root')); m.cleanup();})()`);
  await delay(900);
  assert.deepEqual(await evaluate(page, `${app}.forceGraphModule.getWhatIfState()`), { active: false, sourceNode: null, unblockedCount: 0 }, 'cleanup cancels a pending cascade before freeing WASM');
  clean(page);
  console.log('PASS: directed what-if, resolved prerequisites, displayed gains, force-graph simulation and cancellation');
}

async function journey(page, mobile) {
  await ready(page);
  await waitFor(page, '!!navigator.serviceWorker.controller', 'installed offline service worker controls page');
  // The first controller reloads the document. Wait for the resulting app.
  await delay(500);
  await ready(page);
  assert.deepEqual(await evaluate(page, '[GRAPH_STATE.graph.nodeCount(), GRAPH_STATE.graph.edgeCount()]'), [4, 1]);
  assert.deepEqual(await evaluate(page, '[...GRAPH_STATE.nodeMap.keys()].sort()'), ['browser-closed', 'browser-detail', 'browser-other', 'browser-root'], 'isolated issues populate the actual WASM graph');
  await evaluate(page, `document.getElementById('priority-chart').scrollIntoView({block:'center'})`);
  await waitFor(page, `Chart.getChart('priority-chart')?.width > 0`, 'rendered chart');
  assert.deepEqual(await evaluate(page, `Chart.getChart('priority-chart').data.datasets[0].data`), [0, 2, 1, 0, 0]);
  assert.ok(await evaluate(page, `(() => { const c=document.getElementById('priority-chart'); return new Set(c.getContext('2d').getImageData(0,0,c.width,c.height).data).size > 10; })()`), 'chart painted actual pixels');
  await capture(page, 'charts');
  await click(page, 'a[href="#/issues"]');
  await waitFor(page, `${app}.view === 'issues'`, 'issue route has finished navigation before typing');
  if (mobile) {
    // Existing icon control is exercised through the real pointer path.
    await click(page, 'button[aria-label="Search issues"]');
  }
  await search(page, 'Orchid');
  await resultIDs(page, ['browser-root', 'browser-detail', 'browser-closed']);
  if (mobile) await click(page, 'button[aria-label="Toggle filters"]');
  await click(page, 'button', 'open');
  await resultIDs(page, ['browser-root', 'browser-detail']);
  await capture(page, 'filtered');
  const route = await evaluate(page, 'location.href');
  await reload(page);
  assert.equal(await evaluate(page, 'location.href'), route, 'filter route survives reload');
  await resultIDs(page, ['browser-root', 'browser-detail']);
  // Keyboard activation must open the same issue as pointer activation.
  await evaluate(page, `document.querySelector('[aria-label^="View issue browser-detail:"]').focus()`);
  await key(page, 'Enter');
  await waitFor(page, `${app}.selectedIssue?.id === 'browser-detail'`, 'keyboard issue detail');
  assert.ok(await evaluate(page, `document.body.innerText.includes('Verified comment violet') && document.body.innerText.includes('Reviewer')`), 'actual exported comment shown');
  assert.equal(await evaluate(page, 'window.__unsafeDisplay || false'), false, 'unsafe display string never executes');
  assert.equal(await evaluate(page, `document.querySelector('[x-html="renderMarkdown(selectedIssue.description)"]').querySelectorAll('script,[onerror],[onclick]').length`), 0, 'unsafe nodes/handlers stripped before rendering');
  await click(page, 'button', 'Copy link');
  await waitFor(page, `${app}.copyLinkMessage === 'Link copied'`, 'copy feedback');
  assert.equal(await evaluate(page, 'navigator.clipboard.readText()'), origin + '/#/issue/browser-detail');
  await capture(page, 'detail-copy');
  await click(page, 'button', 'Show Graph');
  await waitFor(page, `!!document.querySelector('[x-ref="depGraph"] svg')`, 'real Mermaid dependency SVG');
  assert.ok(await evaluate(page, `document.querySelector('[x-ref="depGraph"]').textContent.includes('browser-root')`), 'Mermaid contains actual blocker ID');
  await capture(page, 'mermaid');
  await key(page, 'Escape');
  await waitFor(page, `!${app}.selectedIssue`, 'Escape closes detail');
  await click(page, 'a[href="#/graph"]');
  await waitFor(page, `${app}.forceGraphReady && !${app}.forceGraphLoading && !!document.querySelector('#graph-container canvas')`, 'interactive graph rendered');
  const graphIDs = await evaluate(page, `${app}.forceGraphModule.getGraph().graphData().nodes.map(n=>n.id).sort()`);
  assert.deepEqual(graphIDs, ['browser-closed', 'browser-detail', 'browser-other', 'browser-root']);
  // Click an actual force-graph node using its rendered graph-to-screen transform.
  await delay(1000);
  const point = await evaluate(page, `(() => { const g=${app}.forceGraphModule.getGraph(); const n=g.graphData().nodes.find(n=>n.id==='browser-root'); const p=g.graph2ScreenCoords(n.x,n.y); const r=document.querySelector('#graph-container canvas').getBoundingClientRect(); return {x:r.x+p.x,y:r.y+p.y}; })()`);
  for (const type of ['mousePressed', 'mouseReleased']) await send('Input.dispatchMouseEvent', { type, ...point, button: 'left', clickCount: 1 }, page.session);
  await waitFor(page, `${app}.graphDetailNode?.id === 'browser-root'`, 'graph pointer selects actual root node');
  await capture(page, 'graph');
  assert.ok(await evaluate(page, 'document.documentElement.scrollWidth <= innerWidth + 1'), 'no horizontal loss at viewport width');
  await send('Page.navigate', { url: origin + '/#/issue/browser-detail' }, page.session);
  await ready(page);
  await waitFor(page, `${app}.selectedIssue?.id === 'browser-detail'`, 'direct issue route');
  await setOffline(page, true);
  await reload(page);
  await waitFor(page, `${app}.selectedIssue?.id === 'browser-detail'`, 'primed offline route/detail preserved');
  assert.ok(await evaluate(page, `document.body.innerText.includes('Verified comment violet')`), 'comment still visible offline');
  await capture(page, 'offline-detail');
  await setOffline(page, false);
  // A genuinely changed service-worker response triggers browser update/install.
  workerRevision++;
  await evaluate(page, `window.__beforeWorkerUpdate = true; navigator.serviceWorker.getRegistration().then(r=>r.update())`);
  await waitFor(page, '!window.__beforeWorkerUpdate', 'updated service worker reloads client');
  await ready(page);
  await waitFor(page, `${app}.selectedIssue?.id === 'browser-detail'`, 'route preserved after service worker update');
  await capture(page, 'updated');
  clean(page);
  console.log(`ok: ${page.name} charts/search/filter/keyboard/detail/comments/copy/Mermaid/graph/offline/update`);
}
try {
  const stderr = fs.createWriteStream(path.join(artifacts, 'chrome.stderr'));
  chrome = spawn(browser, ['--headless=new', '--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage',
    '--no-first-run', '--disable-background-networking', '--disable-sync', '--disable-component-update',
    '--remote-debugging-port=0', `--user-data-dir=${path.join(artifacts, 'profile')}`,
    '--user-agent=OpenAI File Downloader, XaiImageApiFetch/1.0', 'about:blank'], { stdio: ['ignore', 'ignore', 'pipe'] });
  const endpoint = await new Promise((resolve, reject) => {
    let output = '';
    const timer = setTimeout(() => reject(new Error('Chrome DevTools endpoint missing')), 15000);
    chrome.on('error', reject);
    chrome.stderr.on('data', chunk => {
      stderr.write(chunk); output += chunk;
      const match = output.match(/DevTools listening on (ws:\/\/[^\s]+)/);
      if (match) { clearTimeout(timer); resolve(match[1]); }
    });
  });
  socket = new WebSocket(endpoint);
  await new Promise((resolve, reject) => { socket.onopen = resolve; socket.onerror = reject; });
  socket.onmessage = ({ data }) => {
    const message = JSON.parse(data);
    if (message.id) {
      const entry = pending.get(message.id);
      if (!entry) return;
      pending.delete(message.id); clearTimeout(entry.timer);
      if (message.error) entry.reject(new Error(JSON.stringify(message.error))); else entry.resolve(message.result);
      return;
    }
    if (message.method === 'Target.detachedFromTarget') {
      records.push({ detachedTarget: message });
      const detached = sessions.get(message.params.sessionId);
      if (detached) detached.detached = true;
    }
    if (message.method === 'Target.attachedToTarget') records.push({ attachedTarget: message });
    const page = sessions.get(message.sessionId) || (message.method === 'Target.attachedToTarget'
      ? [...sessions.values()].find(p => !p.worker && p.context === message.params.targetInfo.browserContextId)
      : undefined);
    if (!page) { records.push({ unassignedEvent: message }); return; }
    if (message.method === 'Target.attachedToTarget') {
      const child = { ...page, name: page.name + ':' + message.params.targetInfo.type,
        session: message.params.sessionId, target: message.params.targetInfo.targetId,
        worker: true, owner: page.owner || page };
      sessions.set(child.session, child);
      (async () => {
        // Queue domains before resuming. Runtime.enable's response may itself
        // wait for the paused worker to start, so do not await it in isolation.
        await Promise.all([
          send('Runtime.enable', {}, child.session),
          send('Network.enable', {}, child.session),
          emulateRequests(child, !!child.owner.offline),
          send('Fetch.enable', { patterns: [{ urlPattern: '*' }] }, child.session),
          send('Runtime.runIfWaitingForDebugger', {}, child.session),
        ]);
      })().catch(async err => {
        // A worker whose script cannot be fetched can disappear before its
        // debugger domains finish enabling. A confirmed detached target
        // cannot execute unobserved code; preserve that lifecycle evidence.
        if (/Inspected target navigated or closed|Session with given id not found/.test(err.message)) {
          const { targetInfos } = await send('Target.getTargets');
          if (!targetInfos.some(t => t.targetId === child.target)) {
            child.detached = true;
            records.push({ closedBeforeMonitor: err.message, page: child.name, sessionId: child.session, absentTarget: child.target });
            return;
          }
        }
        monitorErrors.push(err.message);
        records.push({ workerMonitorError: err.message, page: child.name });
        try { await send('Runtime.runIfWaitingForDebugger', {}, child.session); } catch (resumeError) { monitorErrors.push(resumeError.message); }
      });
      return;
    }
    if (['Runtime.consoleAPICalled', 'Runtime.exceptionThrown', 'Log.entryAdded', 'Network.requestWillBeSent', 'Network.responseReceived', 'Network.loadingFailed', 'ServiceWorker.workerErrorReported', 'ServiceWorker.workerVersionUpdated'].includes(message.method)) {
      records.push({ page: page.name, offline: !!(page.owner || page).offline, originListening: server.listening, ...message });
    }
    if (message.method === 'Runtime.exceptionThrown' || (message.method === 'Log.entryAdded' && /Content Security Policy|Refused to|violates.*policy/i.test(message.params.entry.text))) page.errors.push(message.params);
    if (message.method === 'Fetch.requestPaused') {
      const url = message.params.request.url;
      const allowed = url.startsWith(origin + '/') || /^(data:|blob:|about:)/.test(url);
      if (!allowed) page.external.push(url);
      const offlineWorker = page.worker && page.owner.offline;
      // Explicitly fail worker fetches as a second guard against target-local
      // emulation gaps. CacheStorage reads do not issue these network requests.
      const failReason = !allowed ? 'BlockedByClient' : offlineWorker ? 'InternetDisconnected' : null;
      send(failReason ? 'Fetch.failRequest' : 'Fetch.continueRequest', { requestId: message.params.requestId, ...(failReason ? { errorReason: failReason } : {}) }, page.session).catch(err => records.push({ fetchError: err.message }));
    }
  };
  records.push({ browser: await send('Browser.getVersion'), origin, mode });
  if (projectBundle) {
    activeBundle = projectBundle;
    const project = await openPage('project');
    await waitFor(project, `typeof Alpine !== 'undefined' && !${app}.loading && Number.isInteger(${app}.stats.total) && !${app}.error && !${app}.globalError`, 'real project export boot');
    await waitFor(project, '!!navigator.serviceWorker.controller', 'project offline worker installed');
    await delay(500);
    await waitFor(project, `typeof Alpine !== 'undefined' && !${app}.loading && Number.isInteger(${app}.stats.total) && !${app}.error && !${app}.globalError`, 'project boot after worker activation');
    await capture(project, 'boot');
    clean(project);
    console.log('ok: requested project export boot (known fixture journeys follow)');
    activeBundle = bundle;
  }
  const desktop = await openPage('desktop');
  if (mode === 'layout-seeds') {
    await layoutSeedsJourney(desktop);
    await layoutSeedsJourney(await openPage('mobile-360', 360));
    if (updatedBundle) {
      activeBundle = updatedBundle;
      await layoutSeedsJourney(await openPage('edgeless'), true);
    }
  } else if (mode === 'blocking-types') {
    await blockingTypesJourney(desktop);
  } else if (mode === 'readiness') {
    await readinessJourney(desktop);
    await readinessJourney(await openPage('mobile-360', 360));
  } else if (mode === 'hits') {
    await hitsJourney(desktop);
    await hitsJourney(await openPage('mobile-360', 360));
  } else if (mode === 'suggestion-visibility') {
    await suggestionVisibilityJourney(desktop);
    await suggestionVisibilityJourney(await openPage('mobile-360', 360));
  } else if (mode === 'metric-visibility') {
    await metricVisibilityJourney(desktop);
    await metricVisibilityJourney(await openPage('mobile-360', 360));
    if (updatedBundle) {
      activeBundle = updatedBundle;
      const empty = await openPage('empty-ranking');
      await waitFor(empty, `typeof Alpine !== 'undefined' && !${app}.loading && ${app}.stats.total === 0 && ${app}.graphReady`, 'empty graph initialized');
      for (const fn of ['getTopByHITSAuth', 'getTopByHITSHub', 'getTopByKCore', 'getTopByBetweenness', 'getTopByCriticalPath', 'getIssuesBySlack']) {
        assert.deepEqual(await evaluate(empty, `${fn}(10)`), [], `${fn}: actual empty export has no ranked issues`);
      }
      assert.deepEqual(await evaluate(empty, 'getActionableIssues()'), []);
      assert.deepEqual(await evaluate(empty, 'topWhatIf(10)'), []);
      assert.deepEqual(await evaluate(empty, 'getTopKSet(5).items'), []);
      clean(empty);
      console.log('PASS: empty export metric queries');
    }
  } else if (mode === 'what-if') {
    await whatIfJourney(desktop);
    if (updatedBundle) {
      activeBundle = updatedBundle;
      await whatIfJourney(await openPage('closed-rows-excluded'), false);
    }
  } else if (mode === 'offline-only') {
    await ready(desktop);
    await capture(desktop, 'first-load');
    await setOffline(desktop, true);
    await reload(desktop);
    await capture(desktop, 'offline');
    clean(desktop);
    console.log('INCOMPLETE: focused offline check succeeded; full journeys were not run');
    process.exitCode = 2;
  } else {
    await journey(desktop, false);
    const mobile = await openPage('mobile-360', 360);
    await journey(mobile, true);
    // Exercise the genuine optional-module import failure, without replacing
    // the loader or scorer. Small fixtures normally take the size threshold.
    await evaluate(desktop, `HYBRID_WASM_STATE.attempted = false; initHybridWasmScorer(5000)`);
    assert.equal(await evaluate(desktop, 'getHybridWasmStatus().ready'), false);
    assert.match(await evaluate(desktop, 'getHybridWasmStatus().reason'), /fetch|import|module/i);
    await key(desktop, 'Escape');
    await search(desktop, 'Orchid');
    await evaluate(desktop, `(() => {const e=[...document.querySelectorAll('select[x-model="searchMode"]')].find(${visible}); e.value='hybrid'; e.dispatchEvent(new Event('change',{bubbles:true}));})()`);
    await waitFor(desktop, `${app}.issues.length > 0 && ${app}.issues.every(i=>Number.isFinite(i.hybrid_score))`, 'JS hybrid results after optional module failure');
    await resultIDs(desktop, ['browser-root', 'browser-detail', 'browser-closed']);
    assert.equal(await evaluate(desktop, `${app}.searchBackend`), 'substring', 'actual vendored SQLite capability disclosed');
    assert.ok(await evaluate(desktop, `document.body.innerText.includes('Substring text matching')`));
    const scored = await evaluate(desktop, `JSON.parse(JSON.stringify(${app}.issues.map(i=>({score:i.hybrid_score,text:i.text_score,components:i.component_scores}))))`);
    // "Orchid" is a short query: text receives .55 and each remaining default
    // weight is scaled by .45/.60. Recompute without invoking the scorer.
    for (const row of scored) {
      const expected = .55 * row.text + .15 * row.components.pagerank + .1125 * row.components.status + .075 * row.components.impact + .075 * row.components.priority + .0375 * row.components.recency;
      assert.ok(Math.abs(row.score - expected) < 1e-12, `fallback applies the selected hybrid weights: ${JSON.stringify({row, expected})}`);
    }
    await capture(desktop, 'optional-hybrid-fallback');
    clean(desktop);
    await click(desktop, '[aria-label^="View issue browser-detail:"]');
    await waitFor(desktop, `${app}.selectedIssue?.id === 'browser-detail'`, 'detail for denied clipboard');
    await send('Browser.setPermission', { permission: { name: 'clipboard-write' }, setting: 'denied', origin, browserContextId: desktop.context });
    await click(desktop, 'button', 'Copy link');
    await waitFor(desktop, `${app}.copyLinkMessage.startsWith('Could not copy.')`, 'clipboard denial has useful visible feedback');
    await capture(desktop, 'clipboard-denied');
    assert.ok(updatedBundle, 'second real export required for changed-data update');
    const oldCaches = await evaluate(desktop, 'caches.keys()');
    await evaluate(desktop, `caches.open('unrelated-browser-journey').then(c=>c.put('/sentinel',new Response('keep me')))`);
    activeBundle = updatedBundle;
    await evaluate(desktop, `window.__beforeDataUpdate = true; navigator.serviceWorker.getRegistration().then(r=>r.update())`);
    await waitFor(desktop, '!window.__beforeDataUpdate', 'new exported bundle replaces service worker');
    await ready(desktop);
    await waitFor(desktop, `${app}.selectedIssue?.title === 'Orchid searchable detail updated'`, 'new exported database replaces old OPFS cache');
    await setOffline(desktop, true);
    await reload(desktop);
    await waitFor(desktop, `${app}.selectedIssue?.title === 'Orchid searchable detail updated'`, 'new data survives offline reload');
    assert.equal(await evaluate(desktop, `caches.open('unrelated-browser-journey').then(c=>c.match('/sentinel')).then(r=>r.text())`), 'keep me', 'activation preserves unrelated browser cache');
    const newCaches = await evaluate(desktop, 'caches.keys()');
    assert.ok(oldCaches.filter(k=>k.startsWith('beads-viewer-')).every(k=>!newCaches.includes(k)), 'successful activation retires obsolete bundle cache');
    await capture(desktop, 'updated-export-offline');
    clean(desktop);
    // A corrupt update must retain the currently working bundle and database.
    await setOffline(desktop, false);
    changedAsset = '/charts.js'; workerRevision++;
    const beforeFailedUpdate = records.length;
    await evaluate(desktop, `navigator.serviceWorker.getRegistration().then(r=>r.update())`);
    const updateFailureDeadline = Date.now() + 25000;
    while (Date.now() < updateFailureDeadline && !records.slice(beforeFailedUpdate).some(r=>r.method === 'ServiceWorker.workerErrorReported' && JSON.stringify(r.params).includes('Offline asset changed: charts.js'))) await delay(100);
    assert.ok(records.slice(beforeFailedUpdate).some(r=>r.method === 'ServiceWorker.workerErrorReported' && JSON.stringify(r.params).includes('Offline asset changed: charts.js')), 'corrupt update reports exact installer failure');
    await setOffline(desktop, true);
    await reload(desktop);
    await waitFor(desktop, `${app}.selectedIssue?.title === 'Orchid searchable detail updated'`, 'failed update preserves last working bundle offline');
    await capture(desktop, 'failed-update-keeps-working-bundle');
    changedAsset = '';
    activeBundle = bundle;
    const unprimed = await openPage('negative-unprimed-offline', 1280, true);
    await delay(1000);
    assert.equal(await evaluate(unprimed, `typeof Alpine !== 'undefined' && ${app}.stats.total === 4`), false, 'unprimed offline cannot boot');
    await capture(unprimed, 'expected-failure');
    await setOffline(unprimed, false);
    brokenAsset = '/vendor/sql-wasm.wasm';
    assert.ok(fs.existsSync(path.join(bundle, brokenAsset)), 'negative breaks an actual required SQL asset');
    const missing = await openPage('negative-required-asset');
    await waitFor(missing, `typeof Alpine !== 'undefined' && !${app}.loading && !!(${app}.error || ${app}.globalError)`, 'required SQL WASM failure is visible');
    assert.equal(await evaluate(missing, `${app}.graphReady`), false);
    assert.equal(await evaluate(missing, '!!navigator.serviceWorker.controller'), false, 'incomplete bundle never activates worker');
    await capture(missing, 'expected-failure');
    brokenAsset = '';
    changedAsset = '/charts.js';
    const changed = await openPage('negative-changed-after-export');
    await ready(changed);
    const failureDeadline = Date.now() + 25000;
    while (Date.now() < failureDeadline && !records.some(r => r.page === changed.name && r.method === 'ServiceWorker.workerErrorReported' && JSON.stringify(r.params).includes('Offline asset changed: charts.js'))) await delay(100);
    assert.ok(records.some(r => r.page === changed.name && r.method === 'ServiceWorker.workerErrorReported' && JSON.stringify(r.params).includes('Offline asset changed: charts.js')), 'browser reports exact changed-asset install failure');
    assert.equal(await evaluate(changed, '!!navigator.serviceWorker.controller'), false, 'changed asset prevents offline activation');
    await capture(changed, 'expected-incomplete-offline');
    for (const page of sessions.values()) assert.deepEqual(page.external, [], `${page.name}: no external network reliance`);
    assert.deepEqual(monitorErrors, [], 'all browser/worker monitors remained active through negative controls');
    assert.ok(records.some(r=>r.page?.includes(':service_worker') && r.method === 'Network.requestWillBeSent'), 'real worker network requests were observed');
    const offlineWorkerRequests = new Set(records.filter(r=>r.offline && r.page?.includes(':service_worker') && r.method === 'Network.requestWillBeSent').map(r=>r.sessionId + ':' + r.params.requestId));
    assert.ok(offlineWorkerRequests.size > 0, 'offline optional-resource fetch control actually reached the worker');
    assert.equal(records.filter(r=>r.method === 'Network.responseReceived' && offlineWorkerRequests.has(r.sessionId + ':' + r.params.requestId) && r.params.response.status < 400).length, 0, 'offline worker never repairs missing cache entries over the network');
    console.log('PASS: real desktop/mobile/offline/update journeys and required/optional/unsafe/unprimed controls');
  }
} catch (err) {
  for (const page of sessions.values()) await capture(page, 'failure');
  console.error(err.stack); process.exitCode = 1;
} finally {
  if (monitorErrors.length) console.error('Worker monitor errors:', monitorErrors);
  records.push({ pendingCommands: [...pending.values()].map(({method, sessionId}) => ({method, sessionId})) });
  fs.writeFileSync(path.join(artifacts, 'browser-events.json'), JSON.stringify(records, null, 2));
  if (socket?.readyState === WebSocket.OPEN) { try { await send('Browser.close'); } catch (err) { console.error(err.message); } socket.close(); }
  for (const entry of pending.values()) clearTimeout(entry.timer);
  chrome?.kill('SIGTERM');
  await new Promise(resolve => server.close(resolve));
  console.log(`Browser artifacts: ${artifacts}`);
}
