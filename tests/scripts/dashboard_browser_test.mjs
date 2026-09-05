// Explicit opt-in real Chromium journeys. No DOM substitutes or browser packages.
// Artifacts are consumed by the failing assertion/reviewer and retained externally.
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import http from 'node:http';
import { spawn } from 'node:child_process';

const [browser, bundle, artifacts, mode = 'journeys', updatedBundle, projectBundle] = process.argv.slice(2);
assert.ok(browser && bundle && artifacts, 'browser, bundle, artifacts required');
assert.ok(['journeys', 'offline-only'].includes(mode), 'unknown browser test mode');
fs.mkdirSync(artifacts, { recursive: true });
const records = [];
let brokenAsset = '', changedAsset = '', workerRevision = 0, chrome, server, socket;
let activeBundle = bundle;
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
  if (mode === 'offline-only') {
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
