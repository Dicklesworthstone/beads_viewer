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

const [browser, bundle, artifacts, mode = 'journeys', updatedBundle, projectBundle, capabilitiesBundle, multiRepoBundle] = process.argv.slice(2);
assert.ok(browser && bundle && artifacts, 'browser, bundle, artifacts required');
assert.ok(['journeys', 'offline-only', 'blocking-types', 'what-if', 'hits', 'readiness', 'metric-visibility', 'suggestion-visibility', 'layout-seeds', 'precomputed-metrics', 'graph-startup', 'graph-reload', 'history-loading', 'timeline', 'timeline-controls', 'timeline-baseline', 'timeline-removal', 'timeline-animation', 'timeline-sprints', 'timeline-performance'].includes(mode), 'unknown browser test mode');
fs.mkdirSync(artifacts, { recursive: true });
const records = [];
let brokenAsset = '', changedAsset = '', workerRevision = 0, chrome, server, socket;
let activeBundle = bundle;
let layoutVariant = null;
let historyVariant = null;
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
  if (name === '/data/history.json' && mode === 'history-loading' && historyVariant) {
    records.push({historyRequest:req.url,variant:historyVariant});
    res.setHeader('Content-Type', 'application/json');
    if (historyVariant === 'stalled-headers') return;
    if (historyVariant === 'stalled-body') { res.write('{"commits":'); return; }
    if (historyVariant === 'missing') { res.writeHead(404); res.end('missing optional history'); return; }
    res.end(historyVariant === 'malformed' ? '{' : '{"commits":[]}');
    return;
  }
  if (!file.startsWith(path.resolve(activeBundle) + path.sep) || name === brokenAsset || !fs.existsSync(file)) {
    res.writeHead(404); res.end('Required file unavailable'); return;
  }
  res.setHeader('Content-Type', mime[path.extname(file)] || 'application/octet-stream');
  let body = fs.readFileSync(file);
  if (name === '/data/graph_layout.json' && ['layout-seeds','precomputed-metrics','graph-startup'].includes(mode) && layoutVariant) {
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
    if (layoutVariant === 'metric-missing') delete layout.centrality.pagerank[Object.keys(layout.centrality.pagerank)[0]];
    if (layoutVariant === 'metric-invalid') layout.centrality.betweenness[Object.keys(layout.centrality.betweenness)[0]] = 999;
    if (layoutVariant === 'metric-timeout') layout.centrality.status.PageRank.state = 'timeout';
    if (layoutVariant === 'metric-approximate') layout.centrality.status.Betweenness.reason = 'approximate';
    if (layoutVariant === 'metric-old-version') layout.centrality.version = 0;
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
    try {
      const val = await evaluate(page, expression);
      if (val) return val;
    } catch (err) { last = err.message; }
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
async function openPage(name, width = 1280, offline = false, initialPath = '/') {
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
  await send('Page.navigate', { url: origin + initialPath }, sessionId);
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
let clickProbeSeq = 0;
async function click(page, selector, text) {
  const predicate = text === undefined ? visible : `e => (${visible})(e) && e.textContent.trim() === ${JSON.stringify(text)}`;
  await waitFor(page, `[...document.querySelectorAll(${JSON.stringify(selector)})].some(${predicate})`, `visible ${selector} ${text || ''}`);
  const probeKey = `__bvClickRect_${++clickProbeSeq}`;
  await evaluate(page, `(() => {
    window[${JSON.stringify(probeKey)}] = null;
    const el = [...document.querySelectorAll(${JSON.stringify(selector)})].find(${predicate});
    el?.scrollIntoView({ block: 'center', inline: 'nearest' });
  })()`);
  const point = await waitFor(page, `(() => {
    const el = [...document.querySelectorAll(${JSON.stringify(selector)})].find(${predicate});
    if (!el) {
      window[${JSON.stringify(probeKey)}] = null;
      return null;
    }
    let rect = el.getBoundingClientRect();
    if (rect.width <= 0 || rect.height <= 0) {
      window[${JSON.stringify(probeKey)}] = null;
      return null;
    }
    if (rect.top < 0 || rect.bottom > innerHeight || rect.left < 0 || rect.right > innerWidth) {
      el.scrollIntoView({ block: 'center', inline: 'nearest' });
      rect = el.getBoundingClientRect();
      window[${JSON.stringify(probeKey)}] = null;
      return null;
    }
    const x = rect.x + rect.width / 2;
    const y = rect.y + rect.height / 2;
    if (!(x > 0 && x < innerWidth && y > 0 && y < innerHeight)) {
      window[${JSON.stringify(probeKey)}] = null;
      return null;
    }
    const hit = document.elementFromPoint(x, y);
    if (!hit || (hit !== el && !el.contains(hit))) {
      window[${JSON.stringify(probeKey)}] = null;
      return null;
    }
    const rectBounds = [rect.x, rect.y, rect.width, rect.height].map(v => v.toFixed(2)).join(',');
    const prevBounds = window[${JSON.stringify(probeKey)}];
    window[${JSON.stringify(probeKey)}] = rectBounds;
    if (prevBounds !== rectBounds) {
      return null;
    }
    delete window[${JSON.stringify(probeKey)}];
    return { x, y };
  })()`, `stable uncovered ${selector} ${text || ''}`);
  assert.ok(point.x > 0 && point.x < await evaluate(page, 'innerWidth'), `${selector} is horizontally reachable`);
  for (const type of ['mousePressed', 'mouseReleased']) await send('Input.dispatchMouseEvent', { type, ...point, button: 'left', clickCount: 1 }, page.session);
}
async function key(page, key, code = key) {
  const codes = {Enter:13,Escape:27,Home:36,End:35,ArrowLeft:37,ArrowUp:38,ArrowRight:39,ArrowDown:40};
  for (const type of ['keyDown', 'keyUp']) await send('Input.dispatchKeyEvent', { type, key, code, windowsVirtualKeyCode: codes[key] || 0 }, page.session);
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

async function timelineSprintsJourney(page) {
  await ready(page);
  await waitFor(page, '!!navigator.serviceWorker.controller', 'sprint fixture worker controls page');
  await delay(500);
  await ready(page);
  await click(page, 'a[href="#/graph"]');
  await waitFor(page, `${app}.forceGraphReady && !${app}.forceGraphLoading`, 'sprint graph loaded');
  await key(page, 't', 'KeyT');
  const markers = '.timeline-sprints button';
  const texts = await evaluate(page, `[...document.querySelectorAll('${markers}')].map(e=>e.textContent)`);
  assert.deepEqual(texts, ['Start: Review <em>phase</em>', 'End: Review <em>phase</em>'], 'only retained boundaries appear as literal text');
  assert.equal(await evaluate(page, `document.querySelectorAll('.timeline-sprints em').length`), 0, 'sprint names are not interpreted as markup');
  const state = `${app}.forceGraphModule.getTimeTravelState()`;
  await click(page, markers, texts[0]);
  assert.equal(await evaluate(page, `${state}.currentIdx`), 2, 'backdated source commit is selected by boundary time');
  assert.equal(await evaluate(page, `document.querySelector('#tt-date').textContent`), 'Sep 2, 2026');
  await click(page, markers, texts[1]);
  assert.equal(await evaluate(page, `${state}.currentIdx`), 5);
  assert.equal(await evaluate(page, `document.querySelector('#tt-date').textContent`), 'Sep 6, 2026', 'date changes with boundary navigation');
  await capture(page, 'timeline-sprints');
  clean(page);
  console.log(`PASS: ${page.name} sprint boundaries, backdated chronology, date updates and literal names`);
}

async function timelinePerformanceJourney(page) {
  const history = JSON.parse(fs.readFileSync(path.join(bundle,'data/history.json'),'utf8'));
  assert.equal(history.commits.length, 21, 'twenty real recorded transitions');
  const readyLarge = `typeof Alpine !== 'undefined' && !${app}.loading && ${app}.stats.total === 1000 && ${app}.graphReady`;
  await waitFor(page, readyLarge, '1000 exported issues and actual graph WASM ready');
  await waitFor(page, '!!navigator.serviceWorker.controller', 'performance fixture worker controls page');
  await delay(500);
  await waitFor(page, readyLarge, 'large fixture after worker activation');
  await click(page, 'a[href="#/graph"]');
  await waitFor(page, `${app}.forceGraphReady && !${app}.forceGraphLoading && ${app}.graphLoadingStage === null`, 'large graph loaded');
  const state = `${app}.forceGraphModule.getTimeTravelState()`;
  const priorWarmup = await evaluate(page,`${app}.forceGraphModule.getGraph().warmupTicks()`);
  await key(page,'t','KeyT');
  await delay(500);
  await evaluate(page, `document.querySelector('#tt-speed').focus()`);
  await key(page,'End');
  await key(page,'ArrowUp');
  assert.equal(await evaluate(page,`${state}.speed`),5);
  await evaluate(page, `(() => {
    window.__timelinePerf={frames:[],ticks:[],paints:0};
    const p=window.__timelinePerf, arc=CanvasRenderingContext2D.prototype.arc;
    CanvasRenderingContext2D.prototype.arc=function(...args){if(p.active && this.canvas.isConnected)p.paints++;return arc.apply(this,args);};
    document.addEventListener('bv-graph:timeTravelCommit',e=>{
      if(p.active)p.ticks.push({index:e.detail.idx,at:performance.now(),nodes:${app}.forceGraphModule.getGraph().graphData().nodes.length});
    });
    document.querySelector('#tt-play').addEventListener('click',()=>{
      p.active=true;p.started=performance.now();let previous=p.started;
      const sample=t=>{if(!p.active)return;p.frames.push(t-previous);previous=t;requestAnimationFrame(sample);};
      requestAnimationFrame(sample);
    },{once:true});
  })()`);
  await send('Profiler.enable',{},page.session);
  await send('Profiler.start',{},page.session);
  await click(page,'#tt-play');
  await waitFor(page,`${state}.currentIdx===20 && !${state}.playing`,'complete 5x playback',10000);
  await evaluate(page,`new Promise(resolve=>setTimeout(resolve,Math.max(0,window.__timelinePerf.ticks.at(-1).at+250-performance.now())))`);
  const result=await evaluate(page,`(() => {const p=window.__timelinePerf;p.active=false;p.elapsed=performance.now()-p.started;return p;})()`);
  const profile=await send('Profiler.stop',{},page.session);
  fs.writeFileSync(path.join(artifacts,`${page.name}-timeline.cpuprofile`),JSON.stringify(profile.profile));
  const sorted=result.frames.filter(n=>n>=0).sort((a,b)=>a-b);
  const percentile=q=>sorted[Math.min(sorted.length-1,Math.ceil(q*sorted.length)-1)];
  const summary={frames:sorted.length,p95:percentile(.95),p99:percentile(.99),max:sorted.at(-1),paints:result.paints,elapsed:result.elapsed};
  records.push({timelinePerformance:{result,summary},page:page.name});
  console.log(`Timeline performance ${page.name}: ${JSON.stringify(summary)}`);
  assert.equal(result.ticks.length,20,'all twenty transitions observed, no skipped steps');
  assert.ok(result.ticks.every((t,i)=>t.index===i+1 && t.nodes===(i%2===0?950:1000)),'every recorded closure/reopen applied');
  assert.ok(sorted.length>=120 && result.paints>=1000,'measure actual rendering throughout playback');
  // These are browser-animation bounds, not CLI/TUI latency or physical-device claims.
  assert.ok(summary.p95<=33.4 && summary.p99<=50,'1000-node playback frame intervals: p95 <=33.4ms, p99 <=50ms');
  await click(page,'.timeline-close');
  assert.equal(await evaluate(page,`${app}.forceGraphModule.getGraph().warmupTicks()`),priorWarmup,'exit restores configured layout warmup');
  clean(page);
  console.log(`PASS: ${page.name} 1000-node timeline playback frame budget`);
}

async function timelineAnimationJourney(page) {
  await ready(page);
  await waitFor(page, '!!navigator.serviceWorker.controller', 'animation fixture worker controls page');
  await delay(500);
  await ready(page);
  await click(page, 'a[href="#/graph"]');
  await waitFor(page, `${app}.forceGraphReady && !${app}.forceGraphLoading`, 'animation graph loaded');
  // Observe actual visible-canvas arcs without replacing the renderer or clock.
  await evaluate(page, `(() => {
    window.__paintedNode=${app}.forceGraphModule.getGraph().graphData().nodes.find(n=>n.id==='browser-detail');
    window.__timelinePaint=[];
    const arc=CanvasRenderingContext2D.prototype.arc;
    CanvasRenderingContext2D.prototype.arc=function(x,y,r,...args) {
      const n=window.__paintedNode;
      if (this.canvas.isConnected && n && x===n.x && y===n.y) {
        window.__timelinePaint.push({alpha:this.globalAlpha,r,at:performance.now()});
      }
      return arc.call(this,x,y,r,...args);
    };
  })()`);
  const graph = `${app}.forceGraphModule.getGraph()`;
  const state = `${app}.forceGraphModule.getTimeTravelState()`;
  await key(page, 't', 'KeyT');
  await delay(400);
  const appearing = await evaluate(page, 'window.__timelinePaint');
  assert.ok(appearing.some(p=>p.alpha>0 && p.alpha<0.9), 'actual canvas paints intermediate appearance alpha');
  const fullRadius = Math.min(...appearing.filter(p=>p.alpha===1).map(p=>p.r));
  assert.ok(Number.isFinite(fullRadius), 'appearance reaches full size and opacity');
  assert.ok(appearing.some(p=>p.alpha<1 && p.r>fullRadius*1.01), 'appearing node briefly pulses above settled size');
  await evaluate(page, `window.__timelinePaint=[]; document.querySelector('#tt-slider').focus()`);
  await key(page, 'ArrowRight');
  assert.equal(await evaluate(page, `${state}.currentIdx`), 1);
  assert.equal(await evaluate(page, `${graph}.graphData().nodes.some(n=>n.id==='browser-detail')`), false, 'removed node immediately leaves interactive graph');
  await delay(400);
  const disappearing = await evaluate(page, 'window.__timelinePaint');
  assert.ok(disappearing.some(p=>p.alpha>0 && p.alpha<0.9 && p.r<fullRadius), 'removed node actually fades and shrinks on canvas');
  assert.equal(await evaluate(page, `${graph}.autoPauseRedraw()`), true, 'redraw returns to idle policy');
  await evaluate(page, 'window.__timelinePaint=[]');
  await delay(150);
  assert.deepEqual(await evaluate(page, 'window.__timelinePaint'), [], 'no stale disappearing overlay');
  // Reverse an unfinished appearance; it must fade from its current size.
  await key(page, 'ArrowRight');
  await delay(70);
  await key(page, 'ArrowLeft');
  await evaluate(page, 'window.__timelinePaint=[]');
  await delay(300);
  const reversed = await evaluate(page, 'window.__timelinePaint');
  assert.ok(reversed.some(p=>p.alpha>0 && p.alpha<0.9), 'interrupted reverse scrub continues a partial fade');
  assert.ok(reversed.every(p=>p.alpha<0.9), 'reverse scrub does not flash back to full opacity');
  await key(page, 'ArrowRight');
  await delay(50);
  await click(page, '.timeline-close');
  assert.equal(await evaluate(page, `${graph}.autoPauseRedraw()`), true, 'exit cancels transition redraw');
  await evaluate(page, `${graph}.autoPauseRedraw(false)`);
  await key(page, 't', 'KeyT');
  await delay(300);
  assert.equal(await evaluate(page, `${graph}.autoPauseRedraw()`), false, 'settling preserves an existing continuous-redraw policy');
  await click(page, '.timeline-close');
  assert.equal(await evaluate(page, `${graph}.autoPauseRedraw()`), false, 'exit preserves the previous redraw policy');
  await evaluate(page, `${graph}.autoPauseRedraw(true)`);
  await send('Emulation.setEmulatedMedia', {features:[{name:'prefers-reduced-motion',value:'reduce'}]}, page.session);
  await evaluate(page, 'window.__timelinePaint=[]');
  await key(page, 't', 'KeyT');
  await delay(100);
  const reducedPaint = await evaluate(page, 'window.__timelinePaint');
  assert.ok(reducedPaint.length > 0 && reducedPaint.every(p=>p.alpha===1), 'reduced motion paints at full opacity without intermediate appearance');
  await evaluate(page, `window.__timelinePaint=[]; document.querySelector('#tt-slider').focus()`);
  await key(page, 'ArrowRight');
  await evaluate(page, 'window.__timelinePaint=[]');
  await delay(100);
  assert.deepEqual(await evaluate(page, 'window.__timelinePaint'), [], 'reduced motion skips disappearing overlay');
  for (const preset of ['spread','compact']) {
    await evaluate(page,`${app}.forceGraphModule.applyPreset('${preset}')`);
    assert.equal(await evaluate(page,`${graph}.warmupTicks()`),0,'preset changes keep playback warmup disabled');
    await click(page,'.timeline-close');
    assert.equal(await evaluate(page,`${graph}.warmupTicks()`),preset==='spread'?150:50,'exit restores the newly chosen standard or custom preset');
    await key(page,'t','KeyT');
  }
  records.push({timelineAnimation:{appearing,disappearing,reversed,fullRadius},page:page.name});
  await capture(page, 'timeline-animation');
  clean(page);
  console.log(`PASS: ${page.name} real canvas fade/shrink, cleanup and reduced motion`);
}

async function timelineRemovalJourney(page) {
  const history = JSON.parse(fs.readFileSync(path.join(bundle, 'data/history.json')));
  assert.equal(history.commits.length, 7, 'actual recorded deletion/reintroduction fixture');
  for (const i of [1, 4]) {
    assert.deepEqual(history.commits[i].beads_removed, ['browser-detail']);
    assert.equal(history.commits[i].beads_closed, undefined, 'removal is not completion');
  }
  await ready(page);
  await waitFor(page, '!!navigator.serviceWorker.controller', 'removal fixture worker controls page');
  await delay(500);
  await ready(page);
  await click(page, 'a[href="#/graph"]');
  await waitFor(page, `${app}.forceGraphReady && !${app}.forceGraphLoading`, 'removal graph loaded');
  const state = `${app}.forceGraphModule.getTimeTravelState()`;
  await key(page, 't', 'KeyT');
  await waitFor(page, `${state}.active`, 'keyboard enters removal timeline');
  await evaluate(page, `document.querySelector('#tt-slider').focus()`);
  const present = ['browser-detail', 'browser-other', 'browser-root'];
  const absent = ['browser-other', 'browser-root'];
  const expected = [present, absent, present, absent, absent, absent, present];
  // Native range keys exercise both directions, including closed reintroduction.
  for (const indices of [[0,1,2,3,4,5,6], [5,4,3,2,1,0]]) {
    for (const i of indices) {
      if (await evaluate(page, `${state}.currentIdx`) !== i) {
        await key(page, indices[0] === 0 ? 'ArrowRight' : 'ArrowLeft');
      }
      assert.equal(await evaluate(page, `${state}.currentIdx`), i);
      assert.deepEqual(await evaluate(page, `${app}.forceGraphModule.getGraph().graphData().nodes.map(n=>n.id).sort()`), expected[i], `record ${i} visibility`);
      const links = await evaluate(page, `${app}.forceGraphModule.getGraph().graphData().links.length`);
      assert.equal(links, expected[i] === present ? 1 : 0, 'absent nodes have no dangling dependency links');
    }
  }
  await click(page, '.timeline-close');
  assert.equal(await evaluate(page, `${app}.forceGraphModule.getGraph().graphData().nodes.length`), 4);
  await capture(page, 'timeline-removal');
  clean(page);
  console.log(`PASS: ${page.name} deletion, open/closed reintroduction, reopening and reverse replay`);
}

async function timelineBaselineJourney(page) {
  const history = JSON.parse(fs.readFileSync(path.join(bundle, 'data/history.json')));
  assert.equal(history.commits.length, 500, 'real export reaches retained history limit');
  const baseline = ['browser-detail', 'browser-root'];
  assert.deepEqual(history.initial_beads, baseline, 'observed prior open issues seed baseline');
  await ready(page);
  await waitFor(page, '!!navigator.serviceWorker.controller', 'baseline fixture worker controls page');
  await delay(500);
  await ready(page);
  await click(page, 'a[href="#/graph"]');
  await waitFor(page, `${app}.forceGraphReady && !${app}.forceGraphLoading`, 'baseline graph loaded');
  const state = `${app}.forceGraphModule.getTimeTravelState()`;
  const nodes = `${app}.forceGraphModule.getGraph().graphData().nodes.map(n=>n.id).sort()`;
  await key(page, 't', 'KeyT');
  await waitFor(page, `${state}.active`, 'keyboard enters retained timeline');
  assert.equal(await evaluate(page, `${state}.currentIdx`), 0);
  assert.deepEqual(await evaluate(page, nodes), baseline, 'first retained record includes older open issues, excludes closed and future creation');
  await evaluate(page, `document.querySelector('#tt-slider').focus()`);
  await key(page, 'End');
  assert.equal(await evaluate(page, `${state}.currentIdx`), 499);
  assert.deepEqual(await evaluate(page, nodes), ['browser-other', 'browser-root'], 'later creation and closure update baseline');
  await key(page, 'Home');
  assert.deepEqual(await evaluate(page, nodes), baseline, 'rewinding reconstructs initial visibility');
  await click(page, '.timeline-close');
  assert.deepEqual(await evaluate(page, nodes), ['browser-closed', 'browser-detail', 'browser-other', 'browser-root'], 'exit restores all current issues');
  records.push({timelineBaseline:{initial:history.initial_beads,commits:history.commits.length},page:page.name});
  await capture(page, 'timeline-baseline');
  clean(page);
  console.log(`PASS: ${page.name} retained baseline, later creation/closure, rewind and exit`);
}

async function timelineControlsJourney(page) {
  await ready(page);
  await waitFor(page, '!!navigator.serviceWorker.controller', 'controls fixture worker controls page');
  await delay(500);
  await ready(page);
  await click(page, 'a[href="#/graph"]');
  await waitFor(page, `${app}.forceGraphReady && !${app}.forceGraphLoading`, 'controls graph loaded');
  const state = `${app}.forceGraphModule.getTimeTravelState()`;
  await key(page, 't', 'KeyT');
  await evaluate(page, `document.querySelector('#tt-slider').focus()`);
  await key(page, 'End');
  assert.equal(await evaluate(page, `${state}.currentIdx`), 3, 'native range End reaches final commit');
  await key(page, 'Home');
  assert.equal(await evaluate(page, `${state}.currentIdx`), 0, 'native range Home reaches first commit');
  await key(page, 'ArrowRight');
  assert.equal(await evaluate(page, `${state}.currentIdx`), 1, 'native range keys reach an intermediate commit');
  assert.deepEqual(await evaluate(page, `${app}.forceGraphModule.getGraph().graphData().nodes.map(n=>n.id).sort()`),
    ['browser-other','browser-root'], 'scrubbing applies recorded closure');
  await evaluate(page, `document.querySelector('#tt-speed').focus()`);
  await key(page, 'End');
  assert.equal(await evaluate(page, `${state}.speed`), 10, 'native selector chooses fastest speed');
  await key(page, 'ArrowUp');
  const selectedSpeed = await evaluate(page, `${state}.speed`);
  await evaluate(page, `${app}.initForceGraphView()`);
  const refreshed = await evaluate(page, `({speed:${state}.speed,control:document.querySelector('#tt-speed').value})`);
  await evaluate(page, `(async () => {
    const h=await (await fetch('./data/history.json')).json();
    ${app}.forceGraphModule.initTimeTravel({...h,commits:h.commits.slice(0,1)});
    ${app}.forceGraphModule.startTimeTravel();
  })()`);
  const single = await evaluate(page, `({index:${state}.currentIdx,slider:document.querySelector('#tt-slider').value,
    position:document.querySelector('#tt-position').textContent})`);
  records.push({timelineControls:{selectedSpeed,refreshed,single},page:page.name});
  assert.deepEqual({selectedSpeed,refreshed,single},{selectedSpeed:5,refreshed:{speed:5,control:'5'},
    single:{index:0,slider:'0',position:'1 / 1'}}, 'keyboard speed, refreshed preference and single-commit boundary agree');
  await evaluate(page, `${app}.initForceGraphView()`);
  await key(page, 't', 'KeyT');
  await click(page, '#tt-play');
  assert.equal(await evaluate(page, `${state}.playing`), true);
  await waitFor(page, `${state}.currentIdx === 3 && !${state}.playing`, '5x playback reaches end and stops', 2000);
  assert.deepEqual(await evaluate(page, `${app}.forceGraphModule.getGraph().graphData().nodes.map(n=>n.id).sort()`),
    ['browser-detail','browser-other','browser-root']);
  await capture(page, 'timeline-controls');
  clean(page);
  console.log(`PASS: ${page.name} native scrubber, keyboard speed, retained preference, single commit and playback completion`);
}

async function timelineJourney(page) {
  await ready(page);
  await waitFor(page, '!!navigator.serviceWorker.controller', 'timeline fixture worker controls page');
  await delay(500);
  await ready(page);
  await click(page, 'a[href="#/graph"]');
  await waitFor(page, `${app}.forceGraphReady && !${app}.forceGraphLoading`, 'timeline graph loaded');
  const state = `${app}.forceGraphModule.getTimeTravelState()`;
  assert.equal(await evaluate(page, `${state}.totalCommits`), 4, 'actual exported lifecycle history loaded');
  await key(page, 't', 'KeyT');
  await waitFor(page, `${state}.active`, 'keyboard enters timeline');
  const expected = [
    ['browser-detail','browser-other','browser-root'],
    ['browser-other','browser-root'],
    ['browser-other','browser-root'],
    ['browser-detail','browser-other','browser-root'],
  ];
  for (let idx = 0; idx < expected.length; idx++) {
    if (idx) await click(page, '#tt-forward');
    assert.equal(await evaluate(page, `${state}.currentIdx`), idx);
    assert.deepEqual(await evaluate(page, `${app}.forceGraphModule.getGraph().graphData().nodes.map(n=>n.id).sort()`), expected[idx],
      'create, close, closed edit and reopen visibility follows recorded states');
    records.push({timeline:idx,page:page.name,visible:expected[idx]});
  }
  await click(page, '#tt-back');
  assert.deepEqual(await evaluate(page, `${app}.forceGraphModule.getGraph().graphData().nodes.map(n=>n.id).sort()`), expected[2]);
  await click(page, '.timeline-close');
  assert.equal(await evaluate(page, `${state}.active`), false);
  assert.deepEqual(await evaluate(page, `${app}.forceGraphModule.getGraph().graphData().nodes.map(n=>n.id).sort()`),
    ['browser-closed','browser-detail','browser-other','browser-root'], 'exiting restores current graph');
  await key(page, 't', 'KeyT');
  await click(page, '#tt-play');
  await waitFor(page, `${state}.currentIdx > 0`, 'playback advances real history');
  await click(page, '#tt-play');
  const paused = await evaluate(page, state);
  assert.equal(paused.playing, false);
  await delay(1200);
  assert.equal(await evaluate(page, `${state}.currentIdx`), paused.currentIdx, 'pause stops advancement');
  await click(page, '#tt-start');
  await click(page, '#tt-play');
  assert.equal(await evaluate(page, `${state}.playing`), true, 'playback active before data replacement');
  await evaluate(page, `(() => {
    window.__timelineTicks=0;
    document.addEventListener('bv-graph:timeTravelCommit',()=>window.__timelineTicks++);
    const d=getGraphViewData();
    ${app}.forceGraphModule.loadData(d.issues.filter(i=>i.id==='browser-other'),[],null);
  })()`);
  await delay(1200);
  const replaced = await evaluate(page, `({state:${state},ticks:window.__timelineTicks,
    nodes:${app}.forceGraphModule.getGraph().graphData().nodes.map(n=>n.id),
    controls:!!document.querySelector('#time-travel-controls')})`);
  assert.deepEqual(replaced,{state:{active:false,playing:false,currentIdx:0,totalCommits:0,speed:1},
    ticks:0,nodes:['browser-other'],controls:false}, 'old playback cannot overwrite replacement data');
  await evaluate(page, `${app}.initForceGraphView()`);
  assert.equal(await evaluate(page, `${state}.totalCommits`), 4, 'fresh export history can be loaded again');
  await key(page, 't', 'KeyT');
  await click(page, '#tt-play');
  assert.equal(await evaluate(page, `${state}.playing`), true, 'playback active before history replacement');
  await evaluate(page, `(() => {${app}.forceGraphModule.initTimeTravel(null);window.__timelineTicks=0;})()`);
  await delay(1200);
  const cleared = await evaluate(page, `({state:${state},ticks:window.__timelineTicks,
    nodes:${app}.forceGraphModule.getGraph().graphData().nodes.map(n=>n.id).sort(),
    controls:!!document.querySelector('#time-travel-controls')})`);
  assert.deepEqual(cleared,{state:{active:false,playing:false,currentIdx:0,totalCommits:0,speed:1},
    ticks:0,nodes:['browser-closed','browser-detail','browser-other','browser-root'],controls:false},
    'missing replacement history stops playback and restores the current graph');
  await evaluate(page, `${app}.initForceGraphView()`);
  await key(page, 't', 'KeyT');
  await click(page, '#tt-play');
  assert.equal(await evaluate(page, `${state}.playing`), true, 'playback active before cleanup');
  await evaluate(page, `(() => {${app}.forceGraphModule.cleanup();window.__timelineTicks=0;})()`);
  await delay(1200);
  const cleaned = await evaluate(page, `({state:${state},ticks:window.__timelineTicks,
    graph:${app}.forceGraphModule.getGraph(),controls:!!document.querySelector('#time-travel-controls')})`);
  assert.deepEqual(cleaned,{state:{active:false,playing:false,currentIdx:0,totalCommits:0,speed:1},
    ticks:0,graph:null,controls:false}, 'cleanup cancels active playback and releases history');
  records.push({timelineReplacement:replaced,timelineHistoryCleared:cleared,timelineCleanup:cleaned,page:page.name});
  await capture(page, 'timeline');
  clean(page);
  console.log(`PASS: ${page.name} exported timeline navigation, play/pause, replacement recovery and active cleanup`);
}

async function historyLoadingJourney(page) {
  await ready(page);
  await waitFor(page, '!!navigator.serviceWorker.controller', 'history fixture worker controls page');
  await delay(500);
  await ready(page);
  await send('Network.setBypassServiceWorker', {bypass:true}, page.session);
  await click(page, 'a[href="#/graph"]');
  await waitFor(page, `${app}.forceGraphReady && !${app}.forceGraphLoading`, 'initial graph loaded');
  await evaluate(page, `(() => {window.__historyGraphLoads=0;document.addEventListener('bv-graph:dataLoaded',()=>window.__historyGraphLoads++);})()`);
  let loads = 0;
  for (const variant of ['stalled-headers','empty','stalled-body','empty','malformed','missing','empty']) {
    historyVariant = variant;
    const requestsBefore = records.filter(r => r.historyRequest).length;
    const start = Date.now();
    // Start without awaiting: an unbounded request must fail our browser assertion,
    // rather than hang the CDP evaluation waiting for the app's promise.
    await evaluate(page, `void ${app}.initForceGraphView()`);
    await waitFor(page, `${app}.forceGraphReady && !${app}.forceGraphLoading && !${app}.forceGraphError`,
      `${variant}: optional history must release graph loading`, 6000);
    loads++;
    const requests = records.filter(r => r.historyRequest).slice(requestsBefore);
    assert.equal(requests.length, 1, 'refresh reaches the optional history endpoint');
    assert.equal(requests[0].variant, variant);
    assert.equal(await evaluate(page, 'window.__historyGraphLoads'), loads, 'every refresh must actually run');
    assert.deepEqual(await evaluate(page, `${app}.forceGraphModule.getGraph().graphData().nodes.map(n=>n.id).sort()`),
      ['browser-closed','browser-detail','browser-other','browser-root']);
    records.push({historyLoading:variant,page:page.name,elapsedMs:Date.now()-start,loads});
  }
  historyVariant = null;
  await capture(page, 'history-loading');
  clean(page);
  console.log(`PASS: ${page.name} optional history stalls, malformed/missing fallback and refresh recovery`);
}

async function graphReloadJourney(page) {
  await ready(page);
  await waitFor(page, '!!navigator.serviceWorker.controller', 'reload fixture worker controls page');
  await delay(500);
  await ready(page);
  await click(page, 'a[href="#/graph"]');
  await waitFor(page, `${app}.forceGraphReady && !${app}.forceGraphLoading`, 'initial graph loaded');
  const metrics = `(() => {const m=${app}.forceGraphModule.getMetrics();return {
    vectors:{...Object.fromEntries(['pagerank','betweenness','criticalPath','eigenvector','kcore','slack'].map(k=>[k,m[k] === null ? null : Array.from(m[k])])),
      hitsHub:m.hits === null ? null : Array.from(m.hits.hub),hitsAuthority:m.hits === null ? null : Array.from(m.hits.authority)},
    cycles:m.cycles.cycles,articulation:[...m.articulationPoints],
    nodes:${app}.forceGraphModule.getGraph().graphData().nodes.map(n=>({id:n.id,pagerank:n.pagerank,betweenness:n.betweenness}))};})()`;
  const before = await evaluate(page, metrics);
  assert.equal(before.vectors.betweenness.length, 4, 'initial real WASM metric covers four nodes');
  for (let cycle = 0; cycle < 2; cycle++) {
    await evaluate(page, `${app}.forceGraphModule.loadData([],[],null)`);
    const empty = await evaluate(page, metrics);
    assert.equal(empty.vectors.betweenness, null, 'skipped empty-graph metric must not retain old scores');
    for (const [name, values] of Object.entries(empty.vectors)) {
      assert.ok(values === null || values.length === 0, `${name}: no scores from the previous graph`);
    }
    assert.deepEqual(empty.cycles, []);
    assert.deepEqual(empty.articulation, []);
    assert.deepEqual(empty.nodes, []);
    assert.equal(await evaluate(page, `${app}.forceGraphModule.getWasmGraph().nodeCount()`), 0);
    await evaluate(page, `(() => {const d=getGraphViewData();${app}.forceGraphModule.loadData(d.issues,d.dependencies,null);})()`);
    const restored = await evaluate(page, metrics);
    assert.deepEqual(restored, before, 'fresh computation restores exact metrics and node scores');
    records.push({graphReload:cycle,page:page.name,empty,restored});
  }
  const navigation = await evaluate(page, `(() => {
    const m=${app}.forceGraphModule, d=getGraphViewData();
    m.loadData(d.issues,[...d.dependencies,{issue_id:'browser-root',depends_on_id:'browser-detail',type:'blocks'}],null);
    m.initCycleNavigator();m.highlightCycle(0,false);
    const selected=m.getCycleNavigatorState();
    m.loadData(d.issues,d.dependencies,null);
    const afterReload=m.getCycleNavigatorState();
    m.resetCycleNavigator();
    window.__oldPathEvents=[];
    for(const type of ['criticalPathStep','criticalPathComplete']) document.addEventListener('bv-graph:'+type,e=>window.__oldPathEvents.push({type,detail:e.detail}));
    const path=m.animateCriticalPath(true);
    m.loadData(d.issues,d.dependencies,null);
    window.__oldPathEvents=[];
    return {selected,afterReload,path};
  })()`);
  assert.equal(navigation.selected.active,true);
  assert.equal(navigation.selected.cycleCount,1);
  assert.deepEqual([...navigation.selected.currentCycle].sort(),['browser-detail','browser-root']);
  assert.ok(navigation.path?.path.length >= 2, 'real critical path animation started');
  await delay(1000); // Includes both traversal ticks and the delayed completion callback.
  const afterPath = await evaluate(page, `({state:${app}.forceGraphModule.getCriticalPathState(),events:window.__oldPathEvents})`);
  records.push({navigationReload:navigation,afterPath,page:page.name});
  assert.deepEqual({cycle:navigation.afterReload,path:afterPath}, {
    cycle:{active:false,cycleCount:0,currentIndex:0,currentCycle:[],currentPath:''},
    path:{state:{active:false,path:[],length:0,currentStep:0},events:[]}
  }, 'graph replacement drops old cycle IDs and cancels old critical-path callbacks');
  const cleanup = await evaluate(page, `(() => {
    const m=${app}.forceGraphModule,d=getGraphViewData();
    m.loadData(d.issues,[...d.dependencies,{issue_id:'browser-root',depends_on_id:'browser-other',type:'blocks'},
      {issue_id:'browser-other',depends_on_id:'browser-root',type:'blocks'}],null);
    m.initCycleNavigator();m.highlightCycle(0,false);
    const selected=m.getCycleNavigatorState();
    const next=m.nextCycle(),prev=m.prevCycle();
    m.cleanup();
    return {selected,next,prev,after:m.getCycleNavigatorState(),graph:m.getGraph(),path:m.getCriticalPathState()};
  })()`);
  for(const cycle of [cleanup.selected.currentCycle,cleanup.next.cycle,cleanup.prev.cycle]) {
    assert.deepEqual([...cycle].sort(),['browser-other','browser-root'], 'new navigation uses only the replacement cycle');
  }
  assert.deepEqual(cleanup.after,{active:false,cycleCount:0,currentIndex:0,currentCycle:[],currentPath:''});
  assert.equal(cleanup.graph,null);
  assert.equal(cleanup.path.active,false);
  records.push({navigationCleanup:cleanup,page:page.name});
  const cleanupPath = await evaluate(page, `(async () => {
    const m=${app}.forceGraphModule,d=getGraphViewData();
    await m.initGraph('graph-container');
    m.loadData(d.issues,d.dependencies,null);
    const path=m.animateCriticalPath(true);
    m.cleanup();
    window.__oldPathEvents=[];
    return path;
  })()`);
  assert.ok(cleanupPath?.path.length >= 2, 'real animation started immediately before cleanup');
  await delay(1000);
  const afterCleanup = await evaluate(page, `({state:${app}.forceGraphModule.getCriticalPathState(),events:window.__oldPathEvents,graph:${app}.forceGraphModule.getGraph()})`);
  assert.deepEqual(afterCleanup,{state:{active:false,path:[],length:0,currentStep:0},events:[],graph:null},
    'cleanup cancels active traversal and its delayed completion');
  records.push({activePathCleanup:afterCleanup,page:page.name});
  await capture(page, 'graph-reload');
  clean(page);
  console.log(`PASS: ${page.name} fresh metrics, replacement cycle navigation, cancelled path animation and cleanup`);
}

async function graphStartupJourney(page) {
  const readyLarge=`typeof Alpine !== 'undefined' && !${app}.loading && ${app}.stats.total===1000 && ${app}.graphReady`;
  await waitFor(page,readyLarge,'1000-node startup fixture ready');
  await waitFor(page,'!!navigator.serviceWorker.controller','startup worker controls page');
  await delay(500);
  await waitFor(page,readyLarge,'startup fixture after worker activation');
  await send('Network.setBypassServiceWorker',{bypass:true},page.session);
  await click(page,'a[href="#/graph"]');
  await waitFor(page,`${app}.forceGraphReady && !${app}.forceGraphLoading`,'initial graph loaded');
  await evaluate(page,`(() => {
    const g=${app}.forceGraphModule.getGraph(),draw=g.nodeCanvasObject();
    g.nodeCanvasObject(function(node,...args){
      const p=window.__startupSample;
      if(p?.loaded && !p.paint && window.__startupNodes.has(node)
        && Number.isFinite(node.x) && Number.isFinite(node.y))p.paint=performance.now();
      return draw.call(this,node,...args);
    });
  })()`);
  const samples={valid:[],missing:[]};
  for(let round=0;round<5;round++) {
    for(const variant of round%2?['missing','valid']:['valid','missing']) {
      layoutVariant=variant;
      await evaluate(page,`(() => {
        window.__startupSample={started:performance.now()};
        document.addEventListener('bv-graph:dataLoaded',e=>{
          const p=window.__startupSample,g=${app}.forceGraphModule.getGraph();
          p.loaded=performance.now();p.precomputed=e.detail.precomputed;
          p.nodes=g.graphData().nodes.length;p.links=g.graphData().links.length;
          p.warmup=g.warmupTicks();
          window.__startupNodes=new WeakSet(g.graphData().nodes);
        },{once:true});
        ${app}.initForceGraphView();
      })()`);
      await waitFor(page,'!!window.__startupSample.paint','actual first canvas paint');
      const result=await evaluate(page,'window.__startupSample');
      assert.deepEqual([result.nodes,result.links,result.precomputed],[1000,900,variant==='valid'],'same full graph rendered for both startup paths');
      samples[variant].push(result.paint-result.started);
      records.push({graphStartup:{round,variant,result},page:page.name});
      await waitFor(page,`!${app}.forceGraphLoading`,'startup view ready');
    }
  }
  const median=a=>[...a].sort((a,b)=>a-b)[2];
  const summary={samples,seededMedian:median(samples.valid),fallbackMedian:median(samples.missing)};
  records.push({graphStartupSummary:summary,page:page.name});
  console.log(`Graph startup ${page.name}: ${JSON.stringify(summary)}`);
  assert.ok(summary.seededMedian<summary.fallbackMedian*.8,'validated seeds reduce median force-view first-paint latency by at least20%');
  layoutVariant=null;
  clean(page);
  console.log(`PASS: ${page.name} measured precomputed startup benefit with full graph parity`);
}

async function precomputedMetricsJourney(page) {
  await ready(page);
  await waitFor(page, '!!navigator.serviceWorker.controller', 'centrality worker controls page');
  await delay(500);
  await ready(page);
  await send('Network.setBypassServiceWorker', {bypass:true}, page.session);
  const oracle = await evaluate(page, `(() => {
    const g=GRAPH_STATE.graph, pr=g.pagerankDefault(), bt=g.betweenness();
    return Object.fromEntries([...GRAPH_STATE.nodeMap].map(([id,i])=>[id,{pr:pr[i],bt:bt[i]}]));
  })()`);
  await evaluate(page, `(() => {
    const p=window.bvGraphWasm.DiGraph.prototype;
    window.__centralityCalls={};
    for(const name of ['pagerankDefault','betweenness','betweennessApprox']) {
      const original=p[name];
      p[name]=function(...args){window.__centralityCalls[name]=(window.__centralityCalls[name]||0)+1;return original.apply(this,args);};
    }
  })()`);
  const variants = ['valid','metric-missing','metric-invalid','metric-timeout','metric-approximate','metric-old-version','stale-edge','missing','valid'];
  for (const variant of variants) {
    layoutVariant = variant;
    await evaluate(page, `(() => {
      window.__centralityCalls={};window.__centralityLoad=null;
      document.addEventListener('bv-graph:dataLoaded',e=>{
        window.__centralityLoad={reused:e.detail.precomputedMetrics,calls:{...window.__centralityCalls},
          nodes:${app}.forceGraphModule.getGraph().graphData().nodes.map(n=>({id:n.id,pr:n.pagerank,bt:n.betweenness}))};
      },{once:true});
    })()`);
    if (await evaluate(page, `${app}.view !== 'graph'`)) await click(page, 'a[href="#/graph"]');
    else await evaluate(page, `${app}.initForceGraphView()`);
    await waitFor(page, '!!window.__centralityLoad', `${variant} centrality loaded`);
    const result = await evaluate(page, 'window.__centralityLoad');
    const expected = variant === 'valid' ? ['pagerank','betweenness']
      : ['metric-missing','metric-timeout'].includes(variant) ? ['betweenness']
      : ['metric-invalid','metric-approximate'].includes(variant) ? ['pagerank'] : [];
    assert.deepEqual(result.reused, expected, `${variant}: only complete computed centrality is reused`);
    assert.equal(result.calls.pagerankDefault || 0, expected.includes('pagerank') ? 0 : 1);
    assert.equal(result.calls.betweenness || 0, expected.includes('betweenness') ? 0 : 1);
    for (const n of result.nodes) {
      assert.ok(Math.abs(n.pr-oracle[n.id].pr)<0.00001, `${variant}: ${n.id} PageRank agrees within algorithm convergence tolerance`);
      assert.equal(n.bt, oracle[n.id].bt, `${variant}: exact directed betweenness agrees`);
    }
    records.push({centrality:{variant,result},page:page.name});
    await waitFor(page, `!${app}.forceGraphLoading`, 'centrality view finishes');
  }
  clean(page);
  layoutVariant = null;
  console.log(`PASS: ${page.name} centrality reuse, actual avoided WASM calls, parity and fallback`);
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
  assert.ok(records.some(r => typeof r.serverRequest === 'string' && /^\/data\/graph_layout\.json\?_t=\d+$/.test(r.serverRequest)), 'actual cache-busted layout HTTP request');
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
    ['workflow-conditional', 'conditional-blocks'], ['workflow-legacy', ''],
    ['workflow-related', 'team-reference'], ['workflow-waits', 'waits-for'],
  ];
  assert.deepEqual(await evaluate(page, `getGraphViewData().dependencies.map(d=>[d.issue_id,d.type]).sort((a,b)=>a[0].localeCompare(b[0]))`), expected, 'graph query retains blocking and custom non-blocking variants');
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
  await waitFor(page, `${app}.forceGraphReady && !${app}.forceGraphLoading && !!document.querySelector('#graph-container canvas')`, 'force graph draws blocking and non-blocking variants');
  assert.equal(await evaluate(page, `${app}.forceGraphModule.getWasmGraph().edgeCount()`), 5, 'force graph WASM uses blocking edges only');
  assert.deepEqual(await evaluate(page, `${app}.forceGraphModule.getGraph().graphData().links.map(e=>[e.source.id,e.target.id,e.type,e.blocking]).sort((a,b)=>a[0].localeCompare(b[0]))`), expected.map(([id, type])=>[id, 'workflow-root', type || 'blocks', type !== 'team-reference']), 'rendered links preserve endpoint direction and non-blocking flag');
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
    assert.equal(rows.length, 5, `${title}: all five active issues are ranked, excluding closed and tombstone rows`);
    assert.equal(rows.some(r => r.id === 'sim-closed' || r.id === 'sim-deleted'), false, `${title}: excludes resolved issues`);
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
  const slack = await evaluate(page, 'Array.from(GRAPH_STATE.activeGraph.slack()).map((value,idx)=>({id:GRAPH_STATE.activeGraph.nodeId(idx),value})).filter(row=>getIssue(row.id))');
  assert.equal(slack.some(row => row.id === 'sim-closed'), false, 'activeGraph.slack() excludes closed issues');
  assert.ok(slack.some(row=>row.value > 0), 'asymmetric fixture includes flexible work');
  assert.deepEqual(await evaluate(page, 'getIssuesBySlack(100,true).map(i=>i.id).sort()'), slack.filter(row=>row.value===0).map(row=>row.id).sort(), 'zero-slack filter excludes flexible work');
  assert.deepEqual(await evaluate(page, 'getIssuesBySlack(100,false).map(i=>i.slack)'), slack.map(row=>row.value).sort((a,b)=>b-a), 'flexible-work list preserves all actual slack scores');
  const cpHopsAudit = await evaluate(page, `(() => ({
    top0: getTopCriticalPaths(5)[0] || null,
    fmtTop0: formatCriticalPathHops(getTopCriticalPaths(5)[0]),
    fmt2: formatCriticalPathHops({ length: 2 }),
    fmt1: formatCriticalPathHops({ length: 1 }),
    insightsText: document.body.innerText,
  }))()`);
  assert.equal(cpHopsAudit.top0?.length, 3, 'sim-* critical chain has 3 nodes');
  assert.equal(cpHopsAudit.top0?.hops, 2, '3-node critical chain reports 2 hops');
  assert.equal(cpHopsAudit.fmtTop0, '2 hops', 'formatCriticalPathHops formats 3-node chain as 2 hops');
  assert.equal(cpHopsAudit.fmt2, '1 hop', 'formatCriticalPathHops singularizes 2-node chain as 1 hop');
  assert.equal(cpHopsAudit.fmt1, '0 hops', 'formatCriticalPathHops formats 1-node chain as 0 hops');
  assert.ok(
    cpHopsAudit.insightsText.includes('CHAIN (2 HOPS)') || cpHopsAudit.insightsText.includes('Chain (2 hops)'),
    `Insights Top Critical Paths renders 2 hops instead of off-by-one 3 hops: ${cpHopsAudit.insightsText}`
  );
  assert.ok(
    !cpHopsAudit.insightsText.includes('CHAIN (3 HOPS)') && !cpHopsAudit.insightsText.includes('Chain (3 hops)'),
    'Insights Top Critical Paths never renders off-by-one 3 hops on 3-node chain'
  );
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
  for (const [fn, field, vector, graphExpr, suffix] of [
    ['getTopByHITSAuth', 'hits_auth', 'GRAPH_STATE.graph.hitsDefault().authorities', 'GRAPH_STATE.graph', ''],
    ['getTopByHITSHub', 'hits_hub', 'GRAPH_STATE.graph.hitsDefault().hubs', 'GRAPH_STATE.graph', ''],
    ['getTopByKCore', 'kcore', 'GRAPH_STATE.graph.kcore()', 'GRAPH_STATE.graph', ''],
    ['getTopByBetweenness', 'betweenness', 'GRAPH_STATE.graph.betweenness()', 'GRAPH_STATE.graph', ''],
    ['getIssuesBySlack', 'slack', 'GRAPH_STATE.activeGraph.slack()', 'GRAPH_STATE.activeGraph', ',true'],
    ['getIssuesBySlack', 'slack', 'GRAPH_STATE.activeGraph.slack()', 'GRAPH_STATE.activeGraph', ',false'],
  ]) {
    const all = await evaluate(page, `${fn}(100${suffix}).map(i=>({id:i.id,score:i.${field}}))`);
    assert.deepEqual(all.map(i=>i.id).sort(), visibleIDs, `${fn}${suffix}: each actual issue appears once`);
    const raw = await evaluate(page, `Array.from(${vector}).map((score,idx)=>({id:${graphExpr}.nodeId(idx),score}))`);
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
    assert.equal(picks.open_nodes, 1, 'actionable candidate count selects only ready issues');
    const impact = await evaluate(page, `topWhatIf(${limit})`);
    assert.deepEqual(impact.map(i=>[i.issueId,i.result.transitive_unblocks]), [['pick-root',1]], 'top cascade limit is applied after candidate eligibility');
  }
  assert.equal(await evaluate(page, `GRAPH_STATE.graph.topkSet(buildClosedSet(), 5, buildCandidateSet(false)).open_nodes`), 6, 'lifecycle candidate count excludes only the synthetic endpoint');
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
  const tombBurndown = await evaluate(page, `(() => {
    const gd = getGraphViewData();
    const resolved = getResolvedIssueIDs();
    window.bvCharts.init(
      [{ id: 'sim-tomb-only', status: 'open', created_at: '2026-09-01T12:00:00Z', updated_at: '2026-09-01T12:00:00Z' }],
      [{ issue_id: 'sim-tomb-only', depends_on_id: 'sim-deleted', type: 'blocks' }],
      resolved
    );
    const bd = window.bvCharts.computeBurndownData();
    window.bvCharts.init(gd.issues, gd.dependencies, resolved);
    return { open: bd.open, blocked: bd.blocked };
  })()`);
  assert.ok(tombBurndown.open.length > 0 && tombBurndown.open.every(v => v === 1) && tombBurndown.blocked.every(v => v === 0),
    `open issue blocked solely by tombstoned bead is counted as open in burndown: ${JSON.stringify(tombBurndown)}`);
  assert.deepEqual(await evaluate(page, `getActionableIssues().sort()`), ['sim-other', 'sim-root']);
  assert.deepEqual(await evaluate(page, `getTopKSet(5).items.map(i=>[i.issueId,i.marginal_gain])`), [['sim-root', 1], ['sim-child', 1], ['sim-other', 1]]);
  await capture(page, 'what-if-detail');
  await key(page, 'Escape');
  await click(page, 'a[href="#/insights"]');
  await waitFor(page, `[...document.querySelectorAll('span')].some(e=>(${visible})(e) && e.textContent.trim()==='3 potential unblocks')`, 'visible combined priority gain');
  await waitFor(page, `[...document.querySelectorAll('[x-text="item.marginal_gain"]')].filter(${visible}).map(e=>e.textContent).join(',')==='1,1,1'`, 'visible per-pick chained gains');
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
  assert.equal(await evaluate(page, 'document.title'), 'Browser journeys', 'custom --pages-title sets document title');
  assert.equal(await evaluate(page, `document.querySelector('header h1')?.textContent.trim()`), 'Browser journeys', 'custom --pages-title replaces navbar h1');
  assert.equal(await evaluate(page, `document.querySelector('header h1 + p')?.textContent.trim()`), 'Adversarial <smoke> & offline', 'custom --pages-subtitle renders escaped subtitle beneath navbar h1');
  assert.equal(await evaluate(page, `document.querySelectorAll('header smoke').length`), 0, 'subtitle HTML is escaped rather than parsed');
  assert.equal(await evaluate(page, `document.querySelectorAll('#heatmap-container p').length`), 0, 'heatmap placeholder paragraph cleared on init');
  assert.equal(await evaluate(page, `document.getElementById('heatmap-container')?.textContent.includes('Expand to load heatmap') || false`), false, 'heatmap placeholder text removed');
  assert.ok(await evaluate(page, `!!document.getElementById('heatmap-canvas')`), 'heatmap canvas mounted');
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
  // Under Mermaid securityLevel:'strict', clicking a navigable .node must
  // navigate via DOM delegation (no inline onclick/XSS) to the target issue.
  await waitFor(page, `!!document.querySelector('[x-ref="depGraph"] .node[data-bv-issue-id="browser-root"]')`, 'clickable Mermaid blocker node');
  await capture(page, 'mermaid');
  await click(page, '[x-ref="depGraph"] .node[data-bv-issue-id="browser-root"]');
  await waitFor(page, `${app}.selectedIssue?.id === 'browser-root'`, 'Mermaid strict-mode node click navigates to browser-root');
  assert.equal(await evaluate(page, 'window.__unsafeDisplay || false'), false, 'Mermaid click keeps strict XSS safety');
  await key(page, 'Escape');
  await waitFor(page, `!${app}.selectedIssue`, 'Escape closes detail');
  await click(page, 'a[href="#/graph"]');
  await waitFor(page, `${app}.forceGraphReady && !${app}.forceGraphLoading && !!document.querySelector('#graph-container canvas')`, 'interactive graph rendered');
  const graphIDs = await evaluate(page, `${app}.forceGraphModule.getGraph().graphData().nodes.map(n=>n.id).sort()`);
  assert.deepEqual(graphIDs, ['browser-closed', 'browser-detail', 'browser-other', 'browser-root']);
  const acyclicMetrics = await evaluate(page, `(() => {
    const m = ${app}.forceGraphModule, theme = m.THEME?.priority || {};
    const nodes = Object.fromEntries(m.getGraph().graphData().nodes.map(n => [n.id, {slack: n.slack, hitsHub: n.hitsHub, hitsAuth: n.hitsAuth}]));
    return { p0: theme[0], p1: theme[1], legacyP0: theme.p0, nodes };
  })()`);
  assert.equal(acyclicMetrics.p0, '#FF0000', 'THEME.priority[0] defined');
  assert.equal(acyclicMetrics.p1, '#FF5555', 'THEME.priority[1] defined');
  assert.equal(acyclicMetrics.legacyP0, undefined);
  assert.equal(acyclicMetrics.nodes['browser-root'].slack, 0, 'critical-path prerequisite has zero slack');
  assert.equal(acyclicMetrics.nodes['browser-detail'].slack, 0, 'critical-path dependent has zero slack');
  assert.ok(acyclicMetrics.nodes['browser-other'].slack > 0 && acyclicMetrics.nodes['browser-closed'].slack === null, 'active isolated DAG issue receives positive WASM slack while closed issue gets null slack');
  assert.ok(acyclicMetrics.nodes['browser-detail'].hitsHub > 0 && acyclicMetrics.nodes['browser-root'].hitsAuth > 0, 'directed WASM HITS hub/authority attached to force-graph nodes');
  const acyclicGraphExtras = await evaluate(page, `(() => {
    const m = ${app}.forceGraphModule, g = m.getGraph();
    const zeroSlackIds = (${app}.criticalPathSlack || []).map(i => i.id).sort();
    const flexibleSlackIds = (${app}.flexibleSlackIssues || []).map(i => i.id).sort();
    const triageQuickWinIds = ${app}.getTriageQuickWins().map(i => i.id).sort();
    const triageBlockerIds = ${app}.getTriageBlockersToClear().map(i => i.id).sort();
    const triageByTrackLen = ${app}.getTriageRecommendationsByTrack().length;
    const triageByLabelLen = ${app}.getTriageRecommendationsByLabel().length;
    const fmt0 = m.formatSlack(0), fmt1 = m.formatSlack(1), fmtOther = m.formatSlack(${JSON.stringify(acyclicMetrics.nodes['browser-other'].slack)});
    m.animateCriticalPath(false);
    const cpInit = m.getCriticalPathState();
    m.nextCriticalPath();
    const cpNext = m.getCriticalPathState();
    m.prevCriticalPath();
    const cpPrev = m.getCriticalPathState();
    m.resetCriticalPath();
    const cpReset = m.getCriticalPathState();

    m.toggleHeatmap();
    const draw = g.linkCanvasObject();
    const redLinks = g.graphData().links.filter(link => {
      const c = new OffscreenCanvas(256, 256), ctx = c.getContext('2d');
      const a = link.source, b = link.target;
      const scale = 180 / Math.max(Math.abs(b.x - a.x), Math.abs(b.y - a.y), 1);
      ctx.setTransform(scale, 0, 0, scale, 128 - scale * (a.x + b.x) / 2, 128 - scale * (a.y + b.y) / 2);
      draw(link, ctx, scale);
      const px = ctx.getImageData(0, 0, 256, 256).data;
      for (let i = 0; i < px.length; i += 4) {
        if (px[i + 3] >= 40 && px[i] >= 245 && px[i + 1] <= 10 && px[i + 2] <= 10) return true;
      }
      return false;
    }).length;
    m.toggleHeatmap();
    return { zeroSlackIds, flexibleSlackIds, triageQuickWinIds, triageBlockerIds, triageByTrackLen, triageByLabelLen, fmt0, fmt1, fmtOther, cpInit, cpNext, cpPrev, cpReset, redLinks };
  })()`);
  assert.deepEqual(acyclicGraphExtras.zeroSlackIds, ['browser-detail', 'browser-root'], 'acyclic Critical Path Slack populates active zero-slack nodes');
  assert.deepEqual(acyclicGraphExtras.flexibleSlackIds, ['browser-other'], 'acyclic Critical Path Slack populates positive-float flexibleSlackIssues');
  assert.deepEqual(acyclicGraphExtras.triageQuickWinIds, ['browser-other', 'browser-root'], 'getTriageQuickWins surfaces claimable triage.json quick_wins');
  assert.deepEqual(acyclicGraphExtras.triageBlockerIds, ['browser-root'], 'getTriageBlockersToClear surfaces triage.json blockers_to_clear');
  assert.ok(acyclicGraphExtras.triageByTrackLen > 0 && acyclicGraphExtras.triageByLabelLen > 0, `actionable triage.json populates By Track and By Label recommendation groups: ${JSON.stringify(acyclicGraphExtras)}`);
  assert.equal(acyclicGraphExtras.fmt0, '0 hops (critical)');
  assert.equal(acyclicGraphExtras.fmt1, '1 hop');
  assert.match(acyclicGraphExtras.fmtOther, /^\d+ hops?$/, 'positive topological slack formatted in hops');
  assert.equal(acyclicGraphExtras.cpInit.active, true, 'kCriticalPaths stepper activates on acyclic DAG');
  assert.ok(acyclicGraphExtras.cpInit.pathCount >= 1 && acyclicGraphExtras.cpInit.path.length === 2, `kCriticalPaths finds 2-node critical path: ${JSON.stringify(acyclicGraphExtras.cpInit)}`);
  assert.equal(acyclicGraphExtras.cpPrev.currentIndex, 0);
  assert.equal(acyclicGraphExtras.cpReset.active, false);
  assert.equal(acyclicGraphExtras.redLinks, 1, 'acyclic zero-slack edge paints THEME.priority[0] urgency red in heatmap mode');
  // Click an actual force-graph node using its rendered graph-to-screen transform.
  await delay(1000);
  const point = await evaluate(page, `(() => { const g=${app}.forceGraphModule.getGraph(); const n=g.graphData().nodes.find(n=>n.id==='browser-root'); const p=g.graph2ScreenCoords(n.x,n.y); const r=document.querySelector('#graph-container canvas').getBoundingClientRect(); return {x:r.x+p.x,y:r.y+p.y}; })()`);
  for (const type of ['mousePressed', 'mouseReleased']) await send('Input.dispatchMouseEvent', { type, ...point, button: 'left', clickCount: 1 }, page.session);
  await waitFor(page, `${app}.graphDetailNode?.id === 'browser-root'`, 'graph pointer selects actual root node');
  // Wait for Alpine's 300ms slide-in transition (md:translate-x-full -> translate-x-0) to settle before measuring rects.
  await waitFor(page, `(() => {
    const closeBtn = document.querySelector('button[title="Close detail pane (Esc)"]');
    if (!closeBtn || !(${visible})(closeBtn)) return false;
    const r = closeBtn.getBoundingClientRect();
    return r.width > 0 && r.right <= window.innerWidth + 1;
  })()`, 'graphDetailNode drawer finishes slide-in with Close button inside viewport');
  const detailHeaderLayout = await evaluate(page, `(() => {
    const closeBtn = document.querySelector('button[title="Close detail pane (Esc)"]');
    const whatIfBtn = document.querySelector('#graph-whatif-btn');
    const batchBtn = document.querySelector('#graph-whatif-batch-btn');
    const coneBtn = document.querySelector('#graph-focus-cone-btn');
    const rectOf = el => {
      if (!el) return null;
      const r = el.getBoundingClientRect();
      return { visible: (${visible})(el), left: r.left, right: r.right, width: r.width, height: r.height };
    };
    return {
      innerWidth: window.innerWidth,
      close: rectOf(closeBtn),
      whatIf: rectOf(whatIfBtn),
      batch: rectOf(batchBtn),
      cone: rectOf(coneBtn),
    };
  })()`);
  assert.ok(detailHeaderLayout.close?.visible && detailHeaderLayout.close.right <= detailHeaderLayout.innerWidth + 1,
    `${page.name}: graphDetailNode Close button stays visible and unclipped: ${JSON.stringify(detailHeaderLayout)}`);
  assert.ok(detailHeaderLayout.whatIf?.visible && detailHeaderLayout.batch?.visible && detailHeaderLayout.cone?.visible,
    `${page.name}: graphDetailNode action buttons stay visible: ${JSON.stringify(detailHeaderLayout)}`);
  assert.ok(detailHeaderLayout.cone.right <= detailHeaderLayout.innerWidth + 1,
    `${page.name}: graphDetailNode action bar fits inside viewport width: ${JSON.stringify(detailHeaderLayout)}`);
  // Also prove opening #graph-filter-hud popover does NOT displace right-hand layout/heatmap controls off-screen (even at 360px mobile).
  await click(page, '#graph-filter-toggle-btn');
  await waitFor(page, `(${visible})(document.querySelector('#graph-filter-hud'))`, 'Graph Filter HUD popover opens cleanly');
  const popoverLayout = await evaluate(page, `(() => {
    const hud = document.querySelector('#graph-filter-hud')?.getBoundingClientRect();
    const rightPanel = document.querySelector('#graph-right-controls')?.getBoundingClientRect();
    const heatmapBtn = document.querySelector('button[title="Toggle heatmap coloring (H)"]')?.getBoundingClientRect();
    return {
      innerWidth: window.innerWidth,
      hudRight: hud?.right || 0,
      layoutRight: rightPanel?.right || 0,
      heatmapRight: heatmapBtn?.right || 0,
    };
  })()`);
  assert.ok(
    popoverLayout.hudRight > 0 && popoverLayout.layoutRight > 0 && popoverLayout.heatmapRight > 0 &&
    popoverLayout.hudRight <= popoverLayout.innerWidth + 1 &&
    popoverLayout.layoutRight <= popoverLayout.innerWidth + 1 &&
    popoverLayout.heatmapRight <= popoverLayout.innerWidth + 1,
    `${page.name}: opening #graph-filter-hud keeps popover and right-hand Layout/Heatmap controls genuinely visible within viewport: ${JSON.stringify(popoverLayout)}`
  );
  // Capture-phase Escape closes #graph-filter-hud without dismissing graphDetailNode underneath.
  await key(page, 'Escape');
  await waitFor(page, `!${app}.graphFiltersOpen && ${app}.graphDetailNode?.id === 'browser-root'`, 'Escape closes filter HUD without leaking to graphDetailNode');
  // Re-open filter HUD and verify:
  // (a) filtering out selected node closes graphDetailNode and clears selection,
  // (b) status='closed' auto-keeps showClosed=true, unchecking showClosed clears status='closed',
  // (c) status='blocked' matches dependency-blocked open issues (browser-detail),
  // (d) single-repo fixture renders non-clickable plain Repo text or hides #graph-filter-repo.
  await click(page, '#graph-filter-toggle-btn');
  await waitFor(page, `${app}.graphFiltersOpen`, 're-open filter HUD');
  await evaluate(page, `${app}.applyGraphFilter('status', 'closed')`);
  await waitFor(page, `${app}.graphDetailNode === null && ${app}.graphVisibleTotal === 1 && ${app}.graphFilters.showClosed === true`, 'filtering out selected node closes drawer and shows closed node');
  await evaluate(page, `${app}.applyGraphFilter('showClosed', false)`);
  await waitFor(page, `${app}.graphFilters.status === '' && ${app}.graphVisibleClosed === 0 && ${app}.graphVisibleOpen === 3`, 'unchecking showClosed clears status=closed conflict');
  await evaluate(page, `${app}.applyGraphFilter('status', 'blocked')`);
  await waitFor(page, `${app}.graphVisibleTotal === 1 && ${app}.forceGraphModule.getGraph().graphData().nodes[0]?.id === 'browser-detail'`, 'status=blocked matches dependency-blocked open issue');
  await click(page, '#graph-filter-clear');
  await waitFor(page, `${app}.graphVisibleTotal === 4`, 'reset restores all 4 graph nodes');
  await click(page, '#graph-filter-toggle-btn');
  // Verify showIssue on excluded ID displays warning toast instead of blank modal.
  const excludedToastOk = await evaluate(page, `(() => {
    const before = ${app}.toasts.length;
    ${app}.showIssue('excluded-missing-id');
    return ${app}.selectedIssue === null && ${app}.toasts.length > before && ${app}.toasts.some(t => t.message.includes('excluded-missing-id'));
  })()`);
  assert.equal(excludedToastOk, true, `${page.name}: showIssue on excluded ID emits warning toast`);
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

async function capabilitiesJourney(page) {
  const readyCaps = `typeof Alpine !== 'undefined' && !${app}.loading && ${app}.stats.total === 6 && ${app}.graphReady`;
  await waitFor(page, readyCaps, 'six capability issues and actual graph WASM ready');
  await waitFor(page, '!!navigator.serviceWorker.controller', 'capabilities worker controls page');
  await delay(500);
  await waitFor(page, readyCaps, 'capabilities app ready after service worker activation');
  const allCapIDs = ['cap-auth-root', 'cap-auth-ui', 'cap-billing-a', 'cap-billing-b', 'cap-closed-prereq', 'cap-hub-leaf'];

  // 1. Title & Subtitle escaping and navbar rendering.
  assert.equal(await evaluate(page, 'document.title'), 'Capabilities <Title> & Gate');
  assert.equal(await evaluate(page, `document.querySelector('header h1')?.textContent.trim()`), 'Capabilities <Title> & Gate');
  assert.equal(await evaluate(page, `document.querySelector('header h1 + p')?.textContent.trim()`), 'Release <RC-C10> & Proof');
  assert.equal(await evaluate(page, `document.querySelectorAll('header title, header rc-c10').length`), 0, 'escaped title/subtitle markup');

  // 7. Heatmap placeholder cleared + Tombstone burndown check + Priority, Type, and Heatmap chart click-to-filter.
  const capTombstoneBurndown = await evaluate(page, `(() => {
    const orig = getGraphViewData();
    const origResolved = getResolvedIssueIDs();
    DB_STATE.db.run("INSERT INTO issues (id, title, status, priority, issue_type, created_at, updated_at) VALUES ('cap-tomb-prereq', 'Tombstoned prereq', 'tombstone', 2, 'task', '2026-09-01T12:00:00Z', '2026-09-01T12:00:00Z')");
    try {
      const resolvedWithTomb = getResolvedIssueIDs();
      const hasTombInResolved = resolvedWithTomb.has('cap-tomb-prereq');
      window.bvCharts.init(
        [{ id: 'cap-open-after-tomb', status: 'open', created_at: '2026-09-01T12:00:00Z', updated_at: '2026-09-01T12:00:00Z' }],
        [{ issue_id: 'cap-open-after-tomb', depends_on_id: 'cap-tomb-prereq', type: 'blocks' }],
        resolvedWithTomb
      );
      const tombData = window.bvCharts.computeBurndownData();
      window.bvCharts.init(
        [{ id: 'cap-open-missing-prereq', status: 'open', created_at: '2026-09-01T12:00:00Z', updated_at: '2026-09-01T12:00:00Z' }],
        [{ issue_id: 'cap-open-missing-prereq', depends_on_id: 'cap-genuinely-missing', type: 'blocks' }],
        resolvedWithTomb
      );
      const missingData = window.bvCharts.computeBurndownData();
      return {
        hasTombInResolved,
        tombOpen: tombData.open,
        tombBlocked: tombData.blocked,
        missingOpen: missingData.open,
        missingBlocked: missingData.blocked,
      };
    } finally {
      DB_STATE.db.run("DELETE FROM issues WHERE id = 'cap-tomb-prereq'");
      window.bvCharts.init(orig.issues, orig.dependencies, origResolved);
    }
  })()`);
  assert.equal(capTombstoneBurndown.hasTombInResolved, true, 'getResolvedIssueIDs includes tombstoned issues from issues table');
  assert.ok(
    capTombstoneBurndown.tombOpen.length > 0 &&
    capTombstoneBurndown.tombOpen.every(v => v === 1) &&
    capTombstoneBurndown.tombBlocked.every(v => v === 0),
    `open issue whose sole prerequisite is tombstoned is counted as open (not blocked) in burndown: ${JSON.stringify(capTombstoneBurndown)}`
  );
  assert.ok(
    capTombstoneBurndown.missingOpen.length > 0 &&
    capTombstoneBurndown.missingOpen.every(v => v === 0) &&
    capTombstoneBurndown.missingBlocked.every(v => v === 1),
    `open issue with genuinely missing prerequisite stays blocked in burndown: ${JSON.stringify(capTombstoneBurndown)}`
  );
  assert.equal(await evaluate(page, `document.querySelectorAll('#heatmap-container p').length`), 0, 'heatmap placeholder <p> removed');
  assert.ok(await evaluate(page, `document.getElementById('heatmap-canvas')?.width > 0`), 'heatmap canvas painted');
  await waitFor(page, `(${visible})(document.getElementById('priority-chart')) && Chart.getChart('priority-chart')?.width > 0`, 'visible priority chart');
  const p0Point = await evaluate(page, `(() => {
    const c = document.getElementById('priority-chart');
    c.scrollIntoView({ block: 'center' });
    const r = c.getBoundingClientRect();
    const pos = Chart.getChart('priority-chart').getDatasetMeta(0).data[0].tooltipPosition();
    return { x: r.left + pos.x, y: r.top + pos.y };
  })()`);
  await waitFor(page, `document.elementFromPoint(${p0Point.x}, ${p0Point.y}) === document.getElementById('priority-chart')`, 'uncovered priority chart slice');
  for (const type of ['mousePressed', 'mouseReleased']) await send('Input.dispatchMouseEvent', { type, ...p0Point, button: 'left', clickCount: 1 }, page.session);
  await waitFor(page, `${app}.view === 'issues' && JSON.stringify(${app}.filters.priority) === '[0]'`, 'priority chart click filters P0');
  await resultIDs(page, ['cap-auth-root']);

  await click(page, 'a[href="#/"]');
  await waitFor(page, `${app}.view === 'dashboard' && (${visible})(document.getElementById('type-chart')) && Chart.getChart('type-chart')?.width > 0`, 'visible type chart on dashboard');
  const bugBarPoint = await evaluate(page, `(() => {
    const c = document.getElementById('type-chart');
    c.scrollIntoView({ block: 'center' });
    const chart = Chart.getChart('type-chart'), idx = chart.data.labels.indexOf('Bug');
    const bar = chart.getDatasetMeta(0).data[idx], r = c.getBoundingClientRect();
    return { x: r.left + (bar.base + bar.x) / 2, y: r.top + bar.y };
  })()`);
  await waitFor(page, `document.elementFromPoint(${bugBarPoint.x}, ${bugBarPoint.y}) === document.getElementById('type-chart')`, 'uncovered type chart bar');
  for (const type of ['mousePressed', 'mouseReleased']) await send('Input.dispatchMouseEvent', { type, ...bugBarPoint, button: 'left', clickCount: 1 }, page.session);
  await waitFor(page, `${app}.view === 'issues' && JSON.stringify(${app}.filters.type) === '["bug"]'`, 'type chart click filters bug');
  await resultIDs(page, ['cap-auth-root']);

  await click(page, 'a[href="#/"]');
  await waitFor(page, `${app}.view === 'dashboard' && (${visible})(document.getElementById('heatmap-canvas')) && document.getElementById('heatmap-canvas')?.width > 0`, 'visible heatmap canvas');
  const heatmapPoint = await evaluate(page, `(() => {
    const c = document.getElementById('heatmap-canvas');
    c.scrollIntoView({ block: 'center' });
    const r = c.getBoundingClientRect();
    // Sorted labels: ['backend','frontend','observability','ops','security'] -> cellSize=40, row=1 (frontend), col=0 (backend)
    return { x: r.left + 100 + 20, y: r.top + 60 + 40 + 20 };
  })()`);
  await waitFor(page, `document.elementFromPoint(${heatmapPoint.x}, ${heatmapPoint.y}) === document.getElementById('heatmap-canvas')`, 'uncovered heatmap cell');
  for (const type of ['mousePressed', 'mouseReleased']) await send('Input.dispatchMouseEvent', { type, ...heatmapPoint, button: 'left', clickCount: 1 }, page.session);
  await waitFor(page, `${app}.view === 'issues' && JSON.stringify(${app}.filters.labels) === '["frontend","backend"]'`, 'heatmap click filters cross-dependent labels');
  await resultIDs(page, ['cap-auth-root', 'cap-auth-ui', 'cap-billing-a', 'cap-closed-prereq']);
  await click(page, 'button', 'Clear all');
  await resultIDs(page, allCapIDs);

  // 6. Label filter <select> + clickable label chips on issue cards.
  await evaluate(page, `(() => {
    const sel = [...document.querySelectorAll('select[aria-label="Filter by label"]')].find(${visible});
    sel.value = 'security';
    sel.dispatchEvent(new Event('change', { bubbles: true }));
  })()`);
  await waitFor(page, `JSON.stringify(${app}.filters.labels) === '["security"]'`, 'label select filters security');
  await resultIDs(page, ['cap-auth-root']);
  await evaluate(page, `(() => {
    const sel = [...document.querySelectorAll('select[aria-label="Filter by label"]')].find(${visible});
    sel.value = '';
    sel.dispatchEvent(new Event('change', { bubbles: true }));
  })()`);
  await waitFor(page, `${app}.filters.labels.length === 0`, 'label select All clears label filter');
  await resultIDs(page, allCapIDs);

  await click(page, 'span[title="Filter by label: observability"]', 'observability');
  await waitFor(page, `JSON.stringify(${app}.filters.labels) === '["observability"]'`, 'label chip click applies filter');
  assert.equal(await evaluate(page, `${app}.selectedIssue`), null, 'label chip @click.stop does not open issue modal');
  await resultIDs(page, ['cap-hub-leaf']);
  await click(page, 'span[title="Filter by label: observability"]', 'observability');
  await waitFor(page, `${app}.filters.labels.length === 0`, 'clicking active label chip toggles off');
  await resultIDs(page, allCapIDs);

  // 8. Multi-word search tokenization + deterministic lexical scoring + normalized maxPageRank.
  await search(page, 'gateway security');
  await resultIDs(page, ['cap-auth-root']);
  await search(page, 'OAuth alice');
  await resultIDs(page, ['cap-auth-root']);
  await evaluate(page, `(() => {
    const sel = [...document.querySelectorAll('select[x-model="searchMode"]')].find(${visible});
    sel.value = 'hybrid';
    sel.dispatchEvent(new Event('change', { bubbles: true }));
  })()`);
  await waitFor(page, `(() => {
    const el = [...document.querySelectorAll('select[x-model="searchPreset"]')].find(${visible});
    return Boolean(el && el.offsetParent !== null && el.getBoundingClientRect().width > 0);
  })()`, 'hybrid preset visible');
  await search(page, 'OAuth login');
  await waitFor(page, `${app}.issues.length === 3 && ${app}.issues.every(i => Number.isFinite(i.hybrid_score))`, 'multi-word hybrid search scored');
  await resultIDs(page, ['cap-auth-root', 'cap-auth-ui', 'cap-closed-prereq']);
  assert.equal(await evaluate(page, `${app}.searchBackend`), 'substring');
  const hybridAudit = await evaluate(page, `(() => {
    const rawRows = execQuery('SELECT issue_id, pagerank FROM issue_metrics');
    const rawPR = Object.fromEntries(rawRows.map(r => [r.issue_id, r.pagerank]));
    const maxRawPR = Math.max(...rawRows.map(r => r.pagerank));
    const issues = ${app}.issues.map(i => ({ id: i.id, status: i.status, score: i.hybrid_score, text: i.text_score, components: i.component_scores }));
    return JSON.parse(JSON.stringify({ maxRawPR, rawPR, issues }));
  })()`);
  assert.ok(hybridAudit.maxRawPR > 0 && hybridAudit.maxRawPR < 1.0, 'raw SQLite PageRank is < 1 prior to normalization');
  const byID = Object.fromEntries(hybridAudit.issues.map(r => [r.id, r]));
  assert.equal(byID['cap-auth-root'].status, 'open', 'cycle-blocked open issue preserves UI status=open');
  assert.equal(byID['cap-auth-root'].components.status, 0.5, 'cycle-blocked open issue downgrades hybrid status score to 0.5');
  assert.equal(byID['cap-auth-root'].components.impact, 1.0, 'max downstream blocks_count issue scores impact=1.0');
  assert.equal(byID['cap-auth-ui'].status, 'open', 'dependency-blocked open issue preserves UI status=open');
  assert.equal(byID['cap-auth-ui'].components.status, 0.5, 'dependency-blocked open issue downgrades hybrid status score to 0.5');
  assert.equal(byID['cap-closed-prereq'].components.status, 0.1, 'closed issue scores status=0.1');
  assert.ok(byID['cap-auth-root'].text > byID['cap-auth-ui'].text && byID['cap-auth-ui'].text > 0 && byID['cap-auth-root'].text <= 1.0,
    `lexical scorer ranks title token match above description-only match: ${JSON.stringify(byID)}`);
  assert.ok(Math.abs(byID['cap-auth-root'].components.pagerank - 1.0) < 1e-12, 'top PageRank issue normalizes to 1.0');
  for (const row of hybridAudit.issues) {
    assert.ok(row.components.pagerank >= 0 && row.components.pagerank <= 1.0, 'normalized pagerank within [0, 1]');
    assert.ok(Math.abs(row.components.pagerank - (hybridAudit.rawPR[row.id] / hybridAudit.maxRawPR)) < 1e-12, `pagerank normalized by maxPageRank for ${row.id}`);
    const expected = .55 * row.text + .15 * row.components.pagerank + .1125 * row.components.status + .075 * row.components.impact + .075 * row.components.priority + .0375 * row.components.recency;
    assert.ok(Math.abs(row.score - expected) < 1e-12, `hybrid linear combination exact for ${row.id}`);
  }

  // 8b. Expanded FTS5 schema + genuinely exported notes / design / acceptance_criteria / external_ref hyphenated search matching.
  const ftsSQL = await evaluate(page, `execScalar("SELECT sql FROM sqlite_master WHERE name = 'issues_fts'") || ''`);
  for (const col of ['notes', 'design', 'acceptance_criteria', 'external_ref']) {
    assert.ok(ftsSQL.includes(col), `issues_fts schema includes ${col}: ${ftsSQL}`);
  }
  assert.equal(await evaluate(page, `window.beadsViewer.buildFtsMatchQuery('obsidian-note-probe')`), '"obsidian"* "note"* "probe"*');
  assert.equal(await evaluate(page, `window.beadsViewer.buildFtsMatchQuery('naïve-über 東京')`), '"naïve"* "über"* "東京"*');
  assert.equal(await evaluate(page, `window.beadsViewer.buildFtsMatchQuery('nai\\u0308ve')`), '"naïve"*');
  assert.equal(await evaluate(page, `window.beadsViewer.buildFtsMatchQuery('हिंदी')`), '"हिंदी"*');
  assert.equal(await evaluate(page, `window.beadsViewer.buildFtsMatchQuery('___')`), '');
  assert.equal(await evaluate(page, `window.beadsViewer.buildFtsMatchQuery('::: !!!')`), '');
  await search(page, 'obsidian-note-probe');
  await resultIDs(page, ['cap-billing-a']);
  await search(page, 'emerald-criteria-probe');
  await resultIDs(page, ['cap-billing-b']);
  await search(page, 'zircon-design-arch-v2');
  await resultIDs(page, ['cap-auth-ui']);
  await search(page, 'GH-4242');
  await resultIDs(page, ['cap-hub-leaf']);
  // Decomposed NFD ('nai\u0308ve'), Indic combining-mark ('हिंदी'), and underscore-only ('___') / punctuation fallback checks.
  await evaluate(page, `(() => {
    DB_STATE.db.run("INSERT INTO issues (id, title, description, notes, status, priority, issue_type, labels, created_at, updated_at) VALUES ('cap-unicode-nfc', 'Naïve café gateway', 'Unicode NFC हिंदी search target', 'naïve-über हिंदी', 'open', 1, 'task', '[]', '2026-09-01T12:00:00Z', '2026-09-01T12:00:00Z')");
    DB_STATE.db.run("INSERT INTO issues (id, title, description, notes, status, priority, issue_type, labels, created_at, updated_at) VALUES ('cap-underscore-lit', 'Literal underscore case', 'Contains foo___bar marker', 'foo___bar', 'open', 2, 'task', '[]', '2026-09-01T12:00:00Z', '2026-09-01T12:00:00Z')");
    DB_STATE.db.run("INSERT INTO issue_overview_mv (id, title, description, notes, status, priority, issue_type, labels, created_at, updated_at, dependency_state, is_actionable, pagerank, betweenness, eigenvector, critical_path_depth, triage_score, blocks_count, blocked_by_count, blocker_count, dependent_count, critical_depth, in_cycle, comment_count) VALUES ('cap-unicode-nfc', 'Naïve café gateway', 'Unicode NFC हिंदी search target', 'naïve-über हिंदी', 'open', 1, 'task', '[]', '2026-09-01T12:00:00Z', '2026-09-01T12:00:00Z', 'satisfied', 1, 0, 0, 0, 0, 0.25, 0, 0, 0, 0, 0, 0, 0)");
    DB_STATE.db.run("INSERT INTO issue_overview_mv (id, title, description, notes, status, priority, issue_type, labels, created_at, updated_at, dependency_state, is_actionable, pagerank, betweenness, eigenvector, critical_path_depth, triage_score, blocks_count, blocked_by_count, blocker_count, dependent_count, critical_depth, in_cycle, comment_count) VALUES ('cap-underscore-lit', 'Literal underscore case', 'Contains foo___bar marker', 'foo___bar', 'open', 2, 'task', '[]', '2026-09-01T12:00:00Z', '2026-09-01T12:00:00Z', 'satisfied', 1, 0, 0, 0, 0, 0.15, 0, 0, 0, 0, 0, 0, 0)");
  })()`);
  try {
    await search(page, 'nai\u0308ve');
    await resultIDs(page, ['cap-unicode-nfc']);
    assert.ok((await evaluate(page, `${app}.issues[0]?.text_score || 0`)) >= 0.9, 'NFD query scores NFC title prefix match >= 0.9');
    assert.equal(await evaluate(page, `${app}.issues[0]?.component_scores?.status`), 1.0, 'unblocked open issue scores status=1.0 in hybrid search');
    await search(page, 'हिंदी');
    await resultIDs(page, ['cap-unicode-nfc']);
    await search(page, '___');
    await resultIDs(page, ['cap-underscore-lit']);
    assert.equal(await evaluate(page, `${app}.totalIssues`), 1, 'underscore-only LIKE fallback escapes _ wildcard and counts only foo___bar');
    await search(page, ':::');
    await resultIDs(page, []);
    assert.equal(await evaluate(page, `${app}.error || ${app}.globalError || null`), null, 'punctuation fallback query does not error');
  } finally {
    await evaluate(page, `(() => {
      DB_STATE.db.run("DELETE FROM issue_overview_mv WHERE id IN ('cap-unicode-nfc', 'cap-underscore-lit')");
      DB_STATE.db.run("DELETE FROM issues WHERE id IN ('cap-unicode-nfc', 'cap-underscore-lit')");
    })()`);
  }
  // Adversarial operator/colon/hyphen query never crashes FTS5 or UI.
  await search(page, 'bv-123 AND NOT foo:bar');
  assert.equal(await evaluate(page, `${app}.error || ${app}.globalError || null`), null, 'adversarial FTS query does not error');
  await search(page, '');
  await resultIDs(page, allCapIDs);

  // 8b-2. Lexical text mode (!usedFTS) descending text_score ranking, >200-row exact/title recall, status-weight/tie-break parity, Critical Path pill removal, and stale searchQuery reset on KPI stat-card & chart drill-downs.
  await evaluate(page, `(() => {
    const sel = [...document.querySelectorAll('select[x-model="searchMode"]')].find(${visible});
    sel.value = 'text';
    sel.dispatchEvent(new Event('change', { bubbles: true }));
  })()`);
  await evaluate(page, `(() => {
    window.__prevFts5 = window.DB_STATE.fts5;
    window.DB_STATE.fts5 = false;
    DB_STATE.db.run("INSERT INTO issues (id, title, description, status, priority, issue_type, labels, created_at, updated_at) VALUES ('cap-lex-desc', 'Unrelated subsystem', 'Contains sapphireprobe token deep in body', 'open', 0, 'task', '[]', '2026-09-01T12:00:00Z', '2026-09-01T12:00:00Z')");
    DB_STATE.db.run("INSERT INTO issues (id, title, description, status, priority, issue_type, labels, created_at, updated_at) VALUES ('cap-lex-title', 'sapphireprobe primary title', 'Short body', 'open', 3, 'task', '[]', '2026-09-01T12:00:00Z', '2026-09-01T12:00:00Z')");
    DB_STATE.db.run("INSERT INTO issues (id, title, description, status, priority, issue_type, labels, created_at, updated_at) VALUES ('cap-lex-closed-exact', 'sapphireprobe archived fix', 'Closed issue with NULL triage_score', 'closed', 2, 'bug', '[]', '2026-09-01T12:00:00Z', '2026-09-01T12:00:00Z')");
    DB_STATE.db.run("INSERT INTO issue_overview_mv (id, title, description, status, priority, issue_type, labels, created_at, updated_at, dependency_state, is_actionable, pagerank, betweenness, eigenvector, critical_path_depth, triage_score, blocks_count, blocked_by_count, blocker_count, dependent_count, critical_depth, in_cycle, comment_count) VALUES ('cap-lex-desc', 'Unrelated subsystem', 'Contains sapphireprobe token deep in body', 'open', 0, 'task', '[]', '2026-09-01T12:00:00Z', '2026-09-01T12:00:00Z', 'satisfied', 1, 0, 0, 0, 0, 0.5, 0, 0, 0, 0, 0, 0, 0)");
    DB_STATE.db.run("INSERT INTO issue_overview_mv (id, title, description, status, priority, issue_type, labels, created_at, updated_at, dependency_state, is_actionable, pagerank, betweenness, eigenvector, critical_path_depth, triage_score, blocks_count, blocked_by_count, blocker_count, dependent_count, critical_depth, in_cycle, comment_count) VALUES ('cap-lex-title', 'sapphireprobe primary title', 'Short body', 'open', 3, 'task', '[]', '2026-09-01T12:00:00Z', '2026-09-01T12:00:00Z', 'satisfied', 1, 0, 0, 0, 0, 0.1, 0, 0, 0, 0, 0, 0, 0)");
    DB_STATE.db.run("INSERT INTO issue_overview_mv (id, title, description, status, priority, issue_type, labels, created_at, updated_at, dependency_state, is_actionable, pagerank, betweenness, eigenvector, critical_path_depth, triage_score, blocks_count, blocked_by_count, blocker_count, dependent_count, critical_depth, in_cycle, comment_count) VALUES ('cap-lex-closed-exact', 'sapphireprobe archived fix', 'Closed issue with NULL triage_score', 'closed', 2, 'bug', '[]', '2026-09-01T12:00:00Z', '2026-09-01T12:00:00Z', 'satisfied', 0, 0, 0, 0, 0, NULL, 0, 0, 0, 0, 0, 0, 0)");
    for (let k = 0; k < 305; k++) {
      const fid = 'cap-lex-fill-' + String(k).padStart(3, '0');
      DB_STATE.db.run("INSERT INTO issues (id, title, description, status, priority, issue_type, labels, created_at, updated_at) VALUES (?, 'Filler subsystem', 'Body mentions sapphireprobe filler cap-lex-closed-exact context', 'open', 1, 'task', '[]', '2026-09-01T12:00:00Z', '2026-09-01T12:00:00Z')", [fid]);
      DB_STATE.db.run("INSERT INTO issue_overview_mv (id, title, description, status, priority, issue_type, labels, created_at, updated_at, dependency_state, is_actionable, pagerank, betweenness, eigenvector, critical_path_depth, triage_score, blocks_count, blocked_by_count, blocker_count, dependent_count, critical_depth, in_cycle, comment_count) VALUES (?, 'Filler subsystem', 'Body mentions sapphireprobe filler cap-lex-closed-exact context', 'open', 1, 'task', '[]', '2026-09-01T12:00:00Z', '2026-09-01T12:00:00Z', 'satisfied', 1, 0, 0, 0, 0, 0.9, 0, 0, 0, 0, 0, 0, 0)", [fid]);
    }
  })()`);
  try {
    await search(page, 'sapphireprobe');
    await waitFor(page, `${app}.totalIssues === 308 && ${app}.issues.length > 0`, 'lexical text search (>300 matches) recalled title matches ahead of high-triage filler');
    const topTwoLex = await evaluate(page, `${app}.issues.slice(0, 2).map(i => i.id)`);
    assert.deepEqual(topTwoLex, ['cap-lex-closed-exact', 'cap-lex-title'], 'text mode (!usedFTS) recalls NULL-triage closed title match and P3 title match ahead of 305 high-triage description-only rows');
    await search(page, 'cap-lex-closed-exact');
    await waitFor(page, `${app}.issues[0]?.id === 'cap-lex-closed-exact'`, 'exact ID with NULL triage_score recalled first across >300 matches');
    const scorerAudit = await evaluate(page, `(() => {
      const hs = new window.HybridScorer();
      const statusMap = {};
      for (const st of ['open', 'in_progress', ' REVIEW ', 'hooked', 'pinned', 'blocked', 'deferred', 'draft', 'closed', 'tombstone', 'custom_stage']) {
        statusMap[st.trim().toLowerCase()] = hs.normalizeStatus(st);
      }
      const tied = hs.scoreAndRank([
        { id: 'bv-20', status: 'review', priority: 1, pagerank: 0.4, blockerCount: 2, updatedAt: '2026-09-01T12:00:00Z', textScore: 0.75 },
        { id: 'bv-10', status: 'hooked', priority: 1, pagerank: 0.4, blockerCount: 2, updatedAt: '2026-09-01T12:00:00Z', textScore: 0.75 },
      ]).map(r => r.id);
      const batchImpact = window.scoreBatchHybrid([
        { id: 'upstream-blocked', status: 'open', priority: 2, pagerank: 0.5, blocker_count: 5, blocks_count: 0, updatedAt: '2026-09-01T12:00:00Z', textScore: 0.5 },
        { id: 'downstream-unblocker', status: 'open', priority: 2, pagerank: 0.5, blocker_count: 0, blocks_count: 5, updatedAt: '2026-09-01T12:00:00Z', textScore: 0.5 },
      ], window.HYBRID_PRESETS.default).map(r => ({ id: r.id, impact: r.component_scores.impact }));
      return { statusMap, tied, batchImpact };
    })()`);
    assert.deepEqual(scorerAudit.statusMap, {
      open: 1.0,
      in_progress: 0.8,
      review: 0.8,
      hooked: 0.8,
      pinned: 0.2,
      blocked: 0.5,
      deferred: 0.2,
      draft: 0.2,
      closed: 0.1,
      tombstone: 0.0,
      custom_stage: 0.5,
    }, 'HybridScorer status weights match Go pkg/search/normalizers.go');
    assert.deepEqual(scorerAudit.tied, ['bv-10', 'bv-20'], 'HybridScorer tie-breaks equal hybrid_score deterministically by ID');
    assert.equal(scorerAudit.batchImpact[0].id, 'downstream-unblocker', 'scoreBatchHybrid uses downstream blocks_count rather than upstream blocker_count');
  } finally {
    await evaluate(page, `(() => {
      window.DB_STATE.fts5 = window.__prevFts5;
      DB_STATE.db.run("DELETE FROM issue_overview_mv WHERE id LIKE 'cap-lex-%'");
      DB_STATE.db.run("DELETE FROM issues WHERE id LIKE 'cap-lex-%'");
    })()`);
  }
  await search(page, '');
  await resultIDs(page, allCapIDs);

  // 8b-3. SQL label wildcard escaping ('_' and '%'), Go HTML-escaped <,>,& ('\u003c','\u003e','\u0026'), and case-sensitivity across json_each and INSTR fallback.
  const labelAuditSeedRows = [
    ['cap-lbl-us-exact', 'Underscore exact', '["bug_fix"]'],
    ['cap-lbl-us-wild', 'Underscore wildcard probe', '["bugXfix"]'],
    ['cap-lbl-pct-exact', 'Percent exact', '["100%_done"]'],
    ['cap-lbl-pct-wild', 'Percent wildcard probe', '["1000%done"]'],
    ['cap-lbl-html-esc', 'HTML escaped label', '["ui\\u003ccore\\u003e\\u0026v2"]'],
    ['cap-lbl-case-low', 'Lowercase label', '["caseprobe"]'],
    ['cap-lbl-case-up', 'Uppercase label', '["CaseProbe"]'],
    ['cap-lbl-esc-quote', 'Escaped quote label', '["foo\\"bar"]'],
    ['cap-lbl-esc-comma', 'Escaped comma-quote label', '["a\\",\\"b"]'],
  ];
  const labelWildcardAudit = await evaluate(page, `(() => {
    const prevJsonEach = DB_STATE.jsonEach;
    const prevJsonEachDb = DB_STATE.jsonEachDb;
    const rows = ${JSON.stringify(labelAuditSeedRows)};
    for (const [id, title, labels] of rows) {
      DB_STATE.db.run("INSERT INTO issues (id, title, status, priority, issue_type, labels, created_at, updated_at) VALUES (?, ?, 'open', 2, 'task', ?, '2026-09-01T12:00:00Z', '2026-09-01T12:00:00Z')", [id, title, labels]);
      DB_STATE.db.run("INSERT INTO issue_overview_mv (id, title, status, priority, issue_type, labels, created_at, updated_at, dependency_state, is_actionable, pagerank, betweenness, eigenvector, critical_path_depth, triage_score, blocks_count, blocked_by_count, blocker_count, dependent_count, critical_depth, in_cycle, comment_count) VALUES (?, ?, 'open', 2, 'task', ?, '2026-09-01T12:00:00Z', '2026-09-01T12:00:00Z', 'satisfied', 1, 0, 0, 0, 0, 0.2, 0, 0, 0, 0, 0, 0, 0)", [id, title, labels]);
    }
    try {
      const storedHtmlRaw = execScalar("SELECT labels FROM issues WHERE id = 'cap-lbl-html-esc'");
      const hasJsonEach = supportsJsonEach();
      const jsonUs = queryIssues({ labels: ['bug_fix'] }).map(i => i.id);
      const jsonPct = queryIssues({ labels: ['100%_done'] }).map(i => i.id);
      const jsonHtml = queryIssues({ labels: ['ui<core>&v2'] }).map(i => i.id);
      const jsonCaseLow = queryIssues({ labels: ['caseprobe'] }).map(i => i.id);
      const jsonCaseUp = queryIssues({ labels: ['CaseProbe'] }).map(i => i.id);
      const jsonUsCount = countIssues({ labels: ['bug_fix'] });
      const jsonPctCount = countIssues({ labels: ['100%_done'] });
      const jsonHtmlCount = countIssues({ labels: ['ui<core>&v2'] });

      DB_STATE.jsonEach = false;
      DB_STATE.jsonEachDb = DB_STATE.db;
      const likeUs = queryIssues({ labels: ['bug_fix'] }).map(i => i.id);
      const likePct = queryIssues({ labels: ['100%_done'] }).map(i => i.id);
      const instrHtml = queryIssues({ labels: ['ui<core>&v2'] }).map(i => i.id);
      const instrCaseLow = queryIssues({ labels: ['caseprobe'] }).map(i => i.id);
      const instrCaseUp = queryIssues({ labels: ['CaseProbe'] }).map(i => i.id);
      const udfFalseBar = queryIssues({ labels: ['bar'] }).map(i => i.id);
      const udfFalseB = queryIssues({ labels: ['b'] }).map(i => i.id);
      const udfExactQuote = queryIssues({ labels: ['foo"bar'] }).map(i => i.id);
      const udfExactComma = queryIssues({ labels: ['a","b'] }).map(i => i.id);
      const likeUsCount = countIssues({ labels: ['bug_fix'] });
      const likePctCount = countIssues({ labels: ['100%_done'] });
      const instrHtmlCount = countIssues({ labels: ['ui<core>&v2'] });
      const instrCaseLowCount = countIssues({ labels: ['caseprobe'] });
      const instrCaseUpCount = countIssues({ labels: ['CaseProbe'] });
      const closedIds = execQuery("SELECT id FROM issues WHERE LOWER(status) = 'closed' ORDER BY id").map(r => r.id);
      const openIds = execQuery("SELECT id FROM issues WHERE LOWER(status) = 'open' ORDER BY id").map(r => r.id);
      const mixedIds = [...closedIds.slice(0, 1), ...openIds.slice(0, 2)];
      const recalc = recalculateMetrics(mixedIds);
      return {
        storedHtmlRaw,
        hasJsonEach,
        jsonUs, jsonPct, jsonHtml, jsonCaseLow, jsonCaseUp,
        jsonUsCount, jsonPctCount, jsonHtmlCount,
        likeUs, likePct, instrHtml, instrCaseLow, instrCaseUp,
        udfFalseBar, udfFalseB, udfExactQuote, udfExactComma,
        likeUsCount, likePctCount, instrHtmlCount, instrCaseLowCount, instrCaseUpCount,
        recalcNodeCount: recalc ? recalc.nodeCount : -1,
        recalcPrLen: recalc ? recalc.pagerank.length : -1,
        recalcCpLen: recalc ? recalc.criticalPath.length : -1,
        recalcClosedCp: (recalc && closedIds.length > 0) ? recalc.criticalPath[0] : -1,
      };
    } finally {
      DB_STATE.jsonEach = prevJsonEach;
      DB_STATE.jsonEachDb = prevJsonEachDb;
      DB_STATE.db.run("DELETE FROM issue_overview_mv WHERE id LIKE 'cap-lbl-%'");
      DB_STATE.db.run("DELETE FROM issues WHERE id LIKE 'cap-lbl-%'");
    }
  })()`);
  assert.equal(labelWildcardAudit.storedHtmlRaw, '["ui\\u003ccore\\u003e\\u0026v2"]', 'seeded SQLite column stores Go encoding/json \\u003c/\\u003e/\\u0026 literal');
  assert.deepEqual(labelWildcardAudit.jsonUs, ['cap-lbl-us-exact'], 'json_each label filter matches literal underscore only');
  assert.deepEqual(labelWildcardAudit.jsonPct, ['cap-lbl-pct-exact'], 'json_each label filter matches literal % and _ only');
  assert.deepEqual(labelWildcardAudit.jsonHtml, ['cap-lbl-html-esc'], 'json_each label filter matches <, >, &');
  assert.deepEqual(labelWildcardAudit.jsonCaseLow, ['cap-lbl-case-low'], 'json_each label filter is case-sensitive (lowercase)');
  assert.deepEqual(labelWildcardAudit.jsonCaseUp, ['cap-lbl-case-up'], 'json_each label filter is case-sensitive (uppercase)');
  assert.equal(labelWildcardAudit.jsonUsCount, 1);
  assert.equal(labelWildcardAudit.jsonPctCount, 1);
  assert.equal(labelWildcardAudit.jsonHtmlCount, 1);
  assert.deepEqual(labelWildcardAudit.likeUs, ['cap-lbl-us-exact'], 'bv_has_label fallback matches literal _ only');
  assert.deepEqual(labelWildcardAudit.likePct, ['cap-lbl-pct-exact'], 'bv_has_label fallback matches literal % and _ only');
  assert.deepEqual(labelWildcardAudit.instrHtml, ['cap-lbl-html-esc'], 'bv_has_label fallback parses \\u003c, \\u003e, \\u0026 matching Go encoding/json');
  assert.deepEqual(labelWildcardAudit.instrCaseLow, ['cap-lbl-case-low'], 'bv_has_label fallback is case-sensitive (lowercase)');
  assert.deepEqual(labelWildcardAudit.instrCaseUp, ['cap-lbl-case-up'], 'bv_has_label fallback is case-sensitive (uppercase)');
  assert.deepEqual(labelWildcardAudit.udfFalseBar, [], 'bv_has_label fallback does not false-match substring after escaped quote');
  assert.deepEqual(labelWildcardAudit.udfFalseB, [], 'bv_has_label fallback does not false-match substring after escaped quote+comma');
  assert.deepEqual(labelWildcardAudit.udfExactQuote, ['cap-lbl-esc-quote'], 'bv_has_label fallback matches exact label containing literal quote');
  assert.deepEqual(labelWildcardAudit.udfExactComma, ['cap-lbl-esc-comma'], 'bv_has_label fallback matches exact label containing literal quote+comma');
  assert.equal(labelWildcardAudit.recalcCpLen, labelWildcardAudit.recalcNodeCount, 'recalculateMetrics criticalPath array length matches full subgraph nodeCount even with closed issues');
  assert.equal(labelWildcardAudit.recalcCpLen, labelWildcardAudit.recalcPrLen, 'recalculateMetrics criticalPath aligns 1:1 with pagerank array length');
  assert.equal(labelWildcardAudit.recalcClosedCp, 0, 'recalculateMetrics assigns 0 criticalPath height to leading closed issue');
  assert.equal(labelWildcardAudit.likeUsCount, 1);
  assert.equal(labelWildcardAudit.likePctCount, 1);
  assert.equal(labelWildcardAudit.instrHtmlCount, 1);
  assert.equal(labelWildcardAudit.instrCaseLowCount, 1);
  assert.equal(labelWildcardAudit.instrCaseUpCount, 1);

  // URL hash restoration for mode=hybrid&preset=impact-first&critical=1 and Critical Path filter pill removal.
  await evaluate(page, `window.location.hash = '#/issues?mode=hybrid&preset=impact-first&critical=1'`);
  await waitFor(page, `${app}.view === 'issues' && ${app}.searchMode === 'hybrid' && ${app}.searchPreset === 'impact-first' && ${app}.filters.criticalPath === true`, 'URL hash restores mode, preset, and critical=1');
  await click(page, 'button[title="Remove Critical Path filter"]');
  await waitFor(page, `${app}.filters.criticalPath === false && !window.location.hash.includes('critical=1')`, 'Critical Path filter pill clears criticalPath filter and URL hash');
  await evaluate(page, `(() => {
    const sel = [...document.querySelectorAll('select[x-model="searchMode"]')].find(${visible});
    sel.value = 'text';
    sel.dispatchEvent(new Event('change', { bubbles: true }));
    ${app}.searchPreset = 'default';
  })()`);

  // Global search from Dashboard switches to Issues, and returning to #/ Overview + clicking Blocked KPI stat card or chart drill-down clears stale searchQuery!
  await click(page, 'a[href="#/"]');
  await waitFor(page, `${app}.view === 'dashboard'`, 'returned to dashboard for global search');
  const chartThemeSync = await evaluate(page, `(() => {
    const origDark = Boolean(${app}.darkMode);
    ${app}.toggleDarkMode();
    const afterFirstColor = Chart.defaults.color;
    const afterFirstDark = Boolean(${app}.darkMode);
    ${app}.toggleDarkMode();
    const afterSecondColor = Chart.defaults.color;
    const afterSecondDark = Boolean(${app}.darkMode);
    return { origDark, afterFirstDark, afterSecondDark, afterFirstColor, afterSecondColor };
  })()`);
  assert.equal(chartThemeSync.afterSecondDark, chartThemeSync.origDark, 'darkMode restored after theme sync toggle');
  assert.notEqual(chartThemeSync.afterFirstColor, chartThemeSync.afterSecondColor, 'toggleDarkMode updates Chart.defaults.color via BVCharts.applyTheme');
  assert.equal(chartThemeSync.afterFirstDark ? chartThemeSync.afterFirstColor : chartThemeSync.afterSecondColor, '#f8f8f2', 'dark mode Chart.js foreground color');
  assert.equal(chartThemeSync.afterFirstDark ? chartThemeSync.afterSecondColor : chartThemeSync.afterFirstColor, '#1e293b', 'light mode Chart.js foreground color');
  await search(page, 'OAuth');
  await waitFor(page, `${app}.view === 'issues' && ${app}.searchQuery === 'OAuth' && ${app}.issues.length === 3`, 'global header search from Dashboard switches to Issues view');
  await click(page, 'a[href="#/"]');
  await waitFor(page, `${app}.view === 'dashboard'`, 'returned to dashboard with active searchQuery before KPI stat-card click');
  await click(page, 'div[\\@click*="hasBlockers: true"]');
  await waitFor(page, `${app}.view === 'issues' && ${app}.searchQuery === '' && !window.location.hash.includes('q=') && ${app}.filters.hasBlockers === true && ${app}.totalIssues === ${app}.stats.blocked`, 'Blocked KPI stat card clears stale searchQuery and shows all blocked issues');
  await click(page, 'a[href="#/"]');
  await waitFor(page, `${app}.view === 'dashboard'`, 'returned to dashboard before Closed stat card');
  await click(page, 'div[\\@click*="closed"]');
  await waitFor(page, `${app}.view === 'issues' && ${app}.filters.hasBlockers === null && ${app}.searchQuery === '' && JSON.stringify(${app}.filters.status) === '["closed"]'`, 'Closed stat card resets stale hasBlockers filter');
  await resultIDs(page, ['cap-closed-prereq']);
  await search(page, 'OAuth');
  await waitFor(page, `${app}.searchQuery === 'OAuth'`, 'seeded searchQuery before chart drill-down event');
  await evaluate(page, `document.dispatchEvent(new CustomEvent('bv-chart:priorityClick', { detail: { priority: 0 } }))`);
  await waitFor(page, `${app}.view === 'issues' && ${app}.searchQuery === '' && !window.location.hash.includes('q=') && JSON.stringify(${app}.filters.priority) === '[0]'`, 'chart drill-down clears stale searchQuery');
  await click(page, 'button', 'Clear all');
  await waitFor(page, `${app}.filters.priority.length === 0 && ${app}.searchQuery === ''`, 'cleared priority filter');
  await resultIDs(page, allCapIDs);

  // 8b-4. #/issues j/k/Enter keyboard cursor navigation + modal input hijack guard + SQLite metadata columns.
  await evaluate(page, `document.activeElement?.blur?.()`);
  const expectedFirstRowId = await evaluate(page, `${app}.issues[0]?.id`);
  const expectedSecondRowId = await evaluate(page, `${app}.issues[1]?.id`);
  await key(page, 'j', 'KeyJ');
  assert.equal(await evaluate(page, `${app}.focusedIssueIndex`), 0, 'first j highlights row 0 in #/issues');
  await key(page, 'j', 'KeyJ');
  assert.equal(await evaluate(page, `${app}.focusedIssueIndex`), 1, 'second j advances cursor to row 1');
  await key(page, 'k', 'KeyK');
  assert.equal(await evaluate(page, `${app}.focusedIssueIndex`), 0, 'k moves cursor back to row 0');
  assert.ok(expectedFirstRowId && expectedSecondRowId && expectedFirstRowId !== expectedSecondRowId);
  await key(page, 'Enter');
  await waitFor(page, `${app}.selectedIssue?.id === ${JSON.stringify(expectedFirstRowId)}`, 'Enter opens highlighted issue row');
  await key(page, 'Escape');
  await waitFor(page, `!${app}.selectedIssue`, 'Escape closes cursor-opened modal');

  // 8c. Issue Detail Modal external_ref + resolved blocker line-through + input hijack guard + "Show Graph" Mermaid diagram across multiple issues.
  await click(page, '[aria-label^="View issue cap-hub-leaf:"]');
  await waitFor(page, `${app}.selectedIssue?.id === 'cap-hub-leaf'`, 'cap-hub-leaf modal opened');
  const modalInputGuardAudit = await evaluate(page, `(() => {
    const input = [...document.querySelectorAll('input[placeholder="Search issues..."]')].find(${visible});
    if (!input) return { found: false };
    input.focus();
    for (const k of ['j', 'k', 'g']) {
      input.dispatchEvent(new KeyboardEvent('keydown', { key: k, bubbles: true, cancelable: true }));
    }
    const idAfterKeys = ${app}.selectedIssue?.id;
    const depGraphAfterKeys = Boolean(${app}.showDepGraph);
    input.blur();
    const cols = execQuery("PRAGMA table_info(issue_overview_mv)").map(c => c.name);
    return {
      found: true,
      idAfterKeys,
      depGraphAfterKeys,
      hasMetadataCols: ['estimated_minutes', 'created_by', 'close_reason'].every(c => cols.includes(c)),
    };
  })()`);
  assert.equal(modalInputGuardAudit.found, true);
  assert.equal(modalInputGuardAudit.idAfterKeys, 'cap-hub-leaf', 'typing j/k in input while modal open does not hijack issue navigation');
  assert.equal(modalInputGuardAudit.depGraphAfterKeys, false, 'typing g in input while modal open does not toggle dependency graph');
  assert.equal(modalInputGuardAudit.hasMetadataCols, true, 'issue_overview_mv exposes estimated_minutes, created_by, and close_reason');
  await click(page, 'button', 'Show Graph');
  await waitFor(page, `!!document.querySelector('[x-ref="depGraph"] svg')`, 'Show Graph renders Mermaid SVG including resolved prerequisite');
  const modalDepAudit = await evaluate(page, `(() => {
    const blockedBy = ${app}.selectedIssue?.dependencyGroups?.blockedBy || [];
    const closedDep = blockedBy.find(d => d.id === 'cap-closed-prereq');
    const openDep = blockedBy.find(d => d.id === 'cap-auth-root');
    const svgText = document.querySelector('[x-ref="depGraph"] svg')?.textContent || '';
    return {
      extRef: ${app}.selectedIssue?.external_ref || '',
      closedStatus: closedDep?.status,
      openStatus: openDep?.status,
      svgHasClosed: svgText.includes('cap-closed-prereq'),
      svgHasOpen: svgText.includes('cap-auth-root'),
    };
  })()`);
  assert.equal(modalDepAudit.extRef, 'GH-4242');
  assert.equal(modalDepAudit.closedStatus, 'closed');
  assert.equal(modalDepAudit.openStatus, 'open');
  assert.ok(modalDepAudit.svgHasClosed && modalDepAudit.svgHasOpen, `Show Graph renders both active and closed dependencies: ${JSON.stringify(modalDepAudit)}`);
  // Navigate via dependency chip inside modal while showDepGraph === true -> $watch('selectedIssue') re-renders SVG.
  await click(page, 'div.fixed.inset-0.z-50 button', 'cap-auth-root');
  await waitFor(page, `${app}.selectedIssue?.id === 'cap-auth-root' && (document.querySelector('[x-ref="depGraph"] svg')?.textContent || '').includes('cap-auth-ui') && !(document.querySelector('[x-ref="depGraph"] svg')?.textContent || '').includes('cap-closed-prereq')`, 'in-modal dependency chip navigation re-renders Show Graph SVG');
  await key(page, 'Escape');
  await waitFor(page, `!${app}.selectedIssue`, 'Escape closes issue modal');
  // Open 2nd completely unlinked issue (cap-billing-a) and click "Show Graph" -> must render cap-billing-a/b, NOT 1st issue's stale SVG.
  await click(page, '[aria-label^="View issue cap-billing-a:"]');
  await waitFor(page, `${app}.selectedIssue?.id === 'cap-billing-a'`, 'cap-billing-a modal opened');
  await click(page, 'button', 'Show Graph');
  await waitFor(page, `!!document.querySelector('[x-ref="depGraph"] svg')`, 'Show Graph renders Mermaid SVG on 2nd issue');
  const secondSvgText = await evaluate(page, `document.querySelector('[x-ref="depGraph"] svg')?.textContent || ''`);
  assert.ok(secondSvgText.includes('cap-billing-a') && secondSvgText.includes('cap-billing-b'), `2nd issue Show Graph contains cap-billing-a/b: ${secondSvgText}`);
  assert.ok(!secondSvgText.includes('cap-hub-leaf') && !secondSvgText.includes('cap-closed-prereq') && !secondSvgText.includes('cap-auth-root'), `2nd issue Show Graph has no stale 1st issue nodes: ${secondSvgText}`);
  await key(page, 'Escape');
  await waitFor(page, `!${app}.selectedIssue`, 'Escape closes cap-billing-a modal');

  // 4. Insights "Circular Dependencies" Cycle Navigator controls & Influencers Eigenvector rendering.
  await click(page, 'a[href="#/insights"]');
  await waitFor(page, `${app}.view === 'insights' && ${app}.cycleInfo?.hasCycles`, 'dependency cycles detected in Insights');
  assert.equal(await evaluate(page, `${app}.cycleInfo.cycleCount`), 2, 'WASM total_cycles populates cycleCount=2');
  await waitFor(page, `[...document.querySelectorAll('.metric-panel-premium')].some(el => (${visible})(el) && el.textContent.includes('Influencers'))`, 'visible Influencers panel');
  const eigenAudit = await evaluate(page, `(() => {
    const list = ${app}.topByEigenvector || [];
    const panel = [...document.querySelectorAll('.metric-panel-premium')].find(el => (${visible})(el) && el.querySelector('.metric-panel-title')?.textContent.trim() === 'Influencers');
    const subtitle = panel?.querySelector('.metric-panel-subtitle')?.textContent.trim() || '';
    const scores = panel ? [...panel.querySelectorAll('.metric-item-score')].map(el => el.textContent.trim()) : [];
    return {
      count: list.length,
      allActive: list.every(i => i.status !== 'closed' && i.status !== 'tombstone'),
      allPositive: list.length > 0 && list.every(i => Number.isFinite(i.eigenvector) && i.eigenvector > 0),
      firstFormatted: list[0] ? list[0].eigenvector.toFixed(4) : '',
      hasSubtitle: subtitle === 'Eigenvector Centrality',
      rendersFirstScore: list[0] ? scores.includes(list[0].eigenvector.toFixed(4)) : false,
    };
  })()`);
  assert.ok(eigenAudit.count > 0 && eigenAudit.allActive && eigenAudit.allPositive, `topByEigenvector populated with active positive scores: ${JSON.stringify(eigenAudit)}`);
  assert.ok(eigenAudit.hasSubtitle && eigenAudit.rendersFirstScore, `Influencers panel renders Eigenvector Centrality score in DOM: ${JSON.stringify(eigenAudit)}`);
  const slackAndWeightsAudit = await evaluate(page, `(() => {
    const slackItems = ${app}.criticalPathSlack || [];
    const flexItems = ${app}.flexibleSlackIssues || [];
    const topPaths = ${app}.topCriticalPaths || [];
    const weights = ${app}.getTriageWeightComponents();
    const weightSum = weights.reduce((acc, w) => acc + (w.value || 0), 0);
    const hasSlackHeading = document.body.innerText.includes('Critical Path Slack');
    const hasCycleUnavailableMsg = document.body.innerText.includes('Unavailable while dependency cycles exist');
    const defaultImpactCount = (${app}.topImpactIssues || []).length;
    ${app}.toggleIncludeBlockedWhatIf();
    const includeBlockedActive = Boolean(${app}.includeBlockedWhatIf);
    const expandedImpactCount = (${app}.topImpactIssues || []).length;
    ${app}.toggleIncludeBlockedWhatIf();
    return {
      slackCount: slackItems.length,
      flexCount: flexItems.length,
      topPathsCount: topPaths.length,
      weightsLen: weights.length,
      weightSum,
      hasSlackHeading,
      hasCycleUnavailableMsg,
      parallelCutIsArray: Array.isArray(${app}.parallelCutIssues),
      bridgesIsArray: Array.isArray(${app}.bridgeEdges),
      defaultImpactCount,
      includeBlockedActive,
      expandedImpactCount,
    };
  })()`);
  assert.equal(slackAndWeightsAudit.slackCount, 0, 'cyclic graph omits false zero-slack critical path items');
  assert.equal(slackAndWeightsAudit.flexCount, 0, 'cyclic graph omits undefined topological float');
  assert.equal(slackAndWeightsAudit.topPathsCount, 0, 'cyclic graph omits kCriticalPaths');
  assert.ok(slackAndWeightsAudit.hasSlackHeading && slackAndWeightsAudit.hasCycleUnavailableMsg,
    `Critical Path Slack card explains cyclic unavailability: ${JSON.stringify(slackAndWeightsAudit)}`);
  assert.equal(slackAndWeightsAudit.parallelCutIsArray, true);
  assert.equal(slackAndWeightsAudit.bridgesIsArray, true);
  assert.equal(slackAndWeightsAudit.includeBlockedActive, true);
  assert.ok(slackAndWeightsAudit.expandedImpactCount >= slackAndWeightsAudit.defaultImpactCount,
    `allWhatIf includes blocked cascade multipliers: ${JSON.stringify(slackAndWeightsAudit)}`);
  assert.equal(slackAndWeightsAudit.weightsLen, 8, 'getTriageWeightComponents exposes all 8 triage components');
  assert.ok(Math.abs(slackAndWeightsAudit.weightSum - 1.0) < 1e-6, `triage weights sum to 1.0: ${slackAndWeightsAudit.weightSum}`);
  await click(page, 'button', 'Highlight Cycles');
  await waitFor(page, `${app}.view === 'graph' && ${app}.forceGraphReady && !${app}.forceGraphLoading && ${app}.cycleNavigatorActive`, 'Highlight Cycles opens graph and activates navigator');
  assert.equal(await evaluate(page, `${app}.totalCycles`), 2);
  assert.equal(await evaluate(page, `${app}.currentCycleIdx`), 0);
  assert.ok((await evaluate(page, `${app}.currentCyclePath`)).includes('→'), 'active cycle path rendered');

  await click(page, 'a[href="#/insights"]');
  await click(page, 'button[\\@click="nextCycleInGraph()"]');
  await waitFor(page, `${app}.view === 'graph' && ${app}.currentCycleIdx === 1 && ${app}.forceGraphModule.getCycleNavigatorState().currentIndex === 1`, 'nextCycleInGraph selects cycle 2');

  await click(page, 'a[href="#/insights"]');
  await click(page, 'button[\\@click="prevCycleInGraph()"]');
  await waitFor(page, `${app}.view === 'graph' && ${app}.currentCycleIdx === 0 && ${app}.forceGraphModule.getCycleNavigatorState().currentIndex === 0`, 'prevCycleInGraph returns to cycle 1');

  await click(page, 'a[href="#/insights"]');
  await click(page, 'button', 'Zoom to Cycle');
  await waitFor(page, `${app}.view === 'graph' && ${app}.cycleNavigatorActive && (${visible})(document.querySelector('#graph-container'))`, 'focusCycleInGraph navigates to visible graph with cycle focused');
  await evaluate(page, 'document.activeElement?.blur?.()');
  await key(page, 'Escape');
  await waitFor(page, `!${app}.cycleNavigatorActive && !${app}.forceGraphModule.getCycleNavigatorState().active`, 'Escape in Graph View resets active cycleNavigatorState');

  await click(page, 'a[href="#/insights"]');
  await click(page, 'button', 'Highlight Cycles');
  await waitFor(page, `${app}.view === 'graph' && ${app}.cycleNavigatorActive`, 're-enabled cycle navigator before Reset View');
  await click(page, 'a[href="#/insights"]');
  await click(page, 'button', 'Reset View');
  await waitFor(page, `${app}.view === 'graph' && !${app}.cycleNavigatorActive && !${app}.forceGraphModule.getCycleNavigatorState().active`, 'Reset View clears cycle navigator');

  // Ctrl+F / bv-graph:searchRequest focuses #graph-search-input, and multi-node performBatchWhatIf tracks sourceNodes.
  const graphCtrlFAndBatchAudit = await evaluate(page, `new Promise(resolve => {
    const m = ${app}.forceGraphModule;
    const batchRes = m.performBatchWhatIf(['cap-auth-root', 'cap-billing-a']);
    const batchState = m.getWhatIfState();
    const batchIds = m.getBatchWhatIfSourceIds();
    m.resetWhatIf();

    document.activeElement?.blur?.();
    document.body.dispatchEvent(new KeyboardEvent('keydown', { key: 'f', code: 'KeyF', ctrlKey: true, bubbles: true, cancelable: true }));
    setTimeout(() => {
      const focusedId = document.activeElement?.id || '';
      document.activeElement?.blur?.();
      resolve({ batchRes, batchState, batchIds, focusedId });
    }, 60);
  })`);
  assert.equal(graphCtrlFAndBatchAudit.focusedId, 'graph-search-input', 'Ctrl+F in #/graph focuses #graph-search-input');
  assert.deepEqual(graphCtrlFAndBatchAudit.batchIds, ['cap-auth-root', 'cap-billing-a'], 'performBatchWhatIf tracks multi-node source IDs');
  assert.deepEqual(graphCtrlFAndBatchAudit.batchState.sourceNodes, ['cap-auth-root', 'cap-billing-a']);
  assert.ok(graphCtrlFAndBatchAudit.batchRes !== null, 'WASM whatIfCloseBatch returns batch simulation result');

  // [C-5] Overlay state synchronization across setFilter, clearFilters, and resetView in graph.js.
  const overlaySyncAudit = await evaluate(page, `(() => {
    const m = ${app}.forceGraphModule;
    m.initCycleNavigator();
    m.highlightCycle(0, false);
    const firstCycleNodeId = m.getCycleNavigatorState().currentCycle[0];
    m.setFilter('search', firstCycleNodeId);
    const afterKeepFilter = {
      active: m.getCycleNavigatorState().active,
      visibleFlagged: m.getGraph().graphData().nodes.some(n => n.id === firstCycleNodeId && n._cycleHighlight === true),
    };
    m.setFilter('search', 'zzz-nonexistent-overlay-filter');
    const afterEmptyFilter = {
      nodeCount: m.getGraph().graphData().nodes.length,
      cycleActive: m.getCycleNavigatorState().active,
    };
    m.clearFilters();
    const afterClear = {
      nodeCount: m.getGraph().graphData().nodes.length,
      anyStaleCycleFlag: m.getGraph().graphData().nodes.some(n => Boolean(n._cycleHighlight)),
    };

    m.performWhatIf(m.getGraph().graphData().nodes.find(n => n.id === 'cap-auth-root'));
    m.initCycleNavigator();
    m.highlightCycle(0, false);
    m.resetView();
    const afterResetView = {
      whatIfActive: m.getWhatIfState().active,
      cpActive: m.getCriticalPathState().active,
      cycleActive: m.getCycleNavigatorState().active,
      anyOverlayFlag: m.getGraph().graphData().nodes.some(n => Boolean(n._whatIfState || n._cycleHighlight || n._criticalPathHighlight)),
    };
    return { afterKeepFilter, afterEmptyFilter, afterClear, afterResetView };
  })()`);
  assert.deepEqual(overlaySyncAudit.afterKeepFilter, { active: true, visibleFlagged: true }, 'setFilter re-applies _cycleHighlight onto newly prepared visible cycle nodes');
  assert.deepEqual(overlaySyncAudit.afterEmptyFilter, { nodeCount: 0, cycleActive: false }, 'setFilter auto-resets cycle navigator when zero highlighted cycle nodes remain visible');
  assert.deepEqual(overlaySyncAudit.afterClear, { nodeCount: 6, anyStaleCycleFlag: false }, 'clearFilters restores all 6 nodes with zero stale overlay flags');
  assert.deepEqual(overlaySyncAudit.afterResetView, { whatIfActive: false, cpActive: false, cycleActive: false, anyOverlayFlag: false }, 'graph.js resetView() resets What-If, Critical Path, and Cycle Navigator overlays');

  // 2. Galaxy ('5') preset + viewModeChange sync, Time-Travel ('T') button + Space/T, and ArrowRight nodeSelect sync.
  await click(page, 'button[title="Cluster by label (5)"]');
  await waitFor(page, `${app}.currentPreset === 'galaxy' && ${app}.forceGraphModule.getLabelClusterState().active`, 'Galaxy button enables label galaxy');
  await evaluate(page, 'document.activeElement?.blur?.()');
  await key(page, '1', 'Digit1');
  await waitFor(page, `${app}.currentPreset === 'force' && !${app}.forceGraphModule.getLabelClusterState().active`, 'Digit1 syncs toolbar preset back to force');
  await key(page, '5', 'Digit5');
  await waitFor(page, `${app}.currentPreset === 'galaxy' && ${app}.forceGraphModule.getLabelClusterState().active`, 'Digit5 syncs toolbar preset to galaxy');
  await key(page, '1', 'Digit1');
  await waitFor(page, `${app}.currentPreset === 'force'`, 'return to force mode');

  await key(page, '?', 'Slash');
  await waitFor(page, `${app}.showKeyboardHelp`, 'keyboard shortcuts modal opens');
  const helpText = await evaluate(page, `document.body.innerText`);
  assert.ok(helpText.includes('Label Galaxy') && helpText.includes('Time-travel history') && helpText.includes('Play / pause timeline'), 'updated 1-5, T, and Space shortcuts documented');
  await key(page, 'Escape');
  await waitFor(page, `!${app}.showKeyboardHelp`, 'Escape closes keyboard help');

  await waitFor(page, `${app}.timeTravelAvailable`, 'exported git history enables Time Travel button');
  await click(page, 'button[title="Time-travel history (T)"]');
  await waitFor(page, `${app}.timeTravelActive && ${app}.forceGraphModule.getTimeTravelState().active && !!document.querySelector('#time-travel-controls')`, 'Time Travel toolbar button activates timeline');
  assert.equal(await evaluate(page, `${app}.forceGraphModule.getTimeTravelState().totalCommits`), 2);
  await evaluate(page, 'document.activeElement?.blur?.()');
  await key(page, ' ', 'Space');
  await waitFor(page, `(() => { const s = ${app}.forceGraphModule.getTimeTravelState(); return s.playing || s.currentIdx === 1; })()`, 'Space triggers timeline playback');
  await key(page, 't', 'KeyT');
  await waitFor(page, `!${app}.timeTravelActive && !${app}.forceGraphModule.getTimeTravelState().active && !document.querySelector('#time-travel-controls')?.classList.contains('active')`, 'T shortcut exits time-travel');

  // 3. Force-graph WASM hitsHub/hitsAuth + cyclic null slack (no false zero-slack urgency red links on cyclic graph).
  await click(page, 'button[title="Toggle heatmap coloring (H)"]');
  await waitFor(page, `${app}.graphHeatmapActive`, 'heatmap link urgency mode enabled');
  const graphAudit = await evaluate(page, `(() => {
    const m = ${app}.forceGraphModule, g = m.getGraph(), draw = g.linkCanvasObject();
    const nodes = g.graphData().nodes.map(n => ({ id: n.id, slack: n.slack, hitsHub: n.hitsHub, hitsAuth: n.hitsAuth }));
    const nonCycleLinks = g.graphData().links.filter(link => !(link.source.inCycle && link.target.inCycle));
    const redLinks = nonCycleLinks.filter(link => {
      const c = new OffscreenCanvas(256, 256), ctx = c.getContext('2d');
      const a = link.source, b = link.target;
      const scale = 180 / Math.max(Math.abs(b.x - a.x), Math.abs(b.y - a.y), 1);
      ctx.setTransform(scale, 0, 0, scale, 128 - scale * (a.x + b.x) / 2, 128 - scale * (a.y + b.y) / 2);
      draw(link, ctx, scale);
      const px = ctx.getImageData(0, 0, 256, 256).data;
      for (let i = 0; i < px.length; i += 4) {
        if (px[i + 3] >= 40 && px[i] >= 245 && px[i + 1] <= 10 && px[i + 2] <= 10) return true;
      }
      return false;
    }).length;
    return {
      themeP0: m.THEME.priority[0],
      themeP1: m.THEME.priority[1],
      fmtNull: m.formatSlack(null),
      fmtZero: m.formatSlack(0),
      fmtThree: m.formatSlack(3),
      nodes,
      redLinks,
      nonCycleCount: nonCycleLinks.length,
    };
  })()`);
  assert.equal(graphAudit.themeP0, '#FF0000');
  assert.equal(graphAudit.themeP1, '#FF5555');
  assert.equal(graphAudit.fmtNull, '—');
  assert.equal(graphAudit.fmtZero, '0 hops (critical)');
  assert.equal(graphAudit.fmtThree, '3 hops');
  assert.ok(graphAudit.nodes.every(n => n.slack === null && Number.isFinite(n.hitsHub) && Number.isFinite(n.hitsAuth)), 'cyclic graph sets slack=null while keeping finite hitsHub and hitsAuth');
  assert.ok(graphAudit.nodes.some(n => n.hitsHub > 0) && graphAudit.nodes.some(n => n.hitsAuth > 0), 'positive HITS hub and authority scores attached');
  assert.equal(graphAudit.nonCycleCount, 2);
  assert.equal(graphAudit.redLinks, 0, 'cyclic graph never paints false zero-slack red links in heatmap mode');
  await click(page, 'button[title="Toggle heatmap coloring (H)"]');

  // 3b. #graphSizeMetric new options and live node sizing updates.
  const sizeMetricAudit = await evaluate(page, `(() => {
    const sel = document.getElementById('graphSizeMetric');
    const options = sel ? [...sel.options].map(o => o.value) : [];
    const m = ${app}.forceGraphModule;
    const nodes = m.getGraph().graphData().nodes;
    sel.value = 'inDegree';
    sel.dispatchEvent(new Event('change', { bubbles: true }));
    const inDegSizes = nodes.map(n => m.getNodeSize(n));
    const metricAfterInDeg = m.getHeatmapState().metric;
    sel.value = 'eigenvector';
    sel.dispatchEvent(new Event('change', { bubbles: true }));
    const eigenSizes = nodes.map(n => m.getNodeSize(n));
    const metricAfterEigen = m.getHeatmapState().metric;
    sel.value = 'pagerank';
    sel.dispatchEvent(new Event('change', { bubbles: true }));
    return {
      options,
      metricAfterInDeg,
      metricAfterEigen,
      inDegValid: inDegSizes.every(v => Number.isFinite(v) && v >= 4),
      eigenValid: eigenSizes.every(v => Number.isFinite(v) && v >= 4),
      inDegSpread: Math.max(...inDegSizes) > Math.min(...inDegSizes),
    };
  })()`);
  assert.deepEqual(sizeMetricAudit.options, ['pagerank', 'betweenness', 'eigenvector', 'inDegree', 'outDegree', 'criticalPath', 'kcore', 'hitsHub', 'hitsAuth', 'slack'], '#graphSizeMetric exposes all 10 graph metrics');
  assert.equal(sizeMetricAudit.metricAfterInDeg, 'inDegree');
  assert.equal(sizeMetricAudit.metricAfterEigen, 'eigenvector');
  assert.ok(sizeMetricAudit.inDegValid && sizeMetricAudit.eigenValid && sizeMetricAudit.inDegSpread, `getNodeSize scales cleanly across sizeMetric options: ${JSON.stringify(sizeMetricAudit)}`);

  await evaluate(page, `${app}.graphDetailNode = null; ${app}.forceGraphModule.clearSelection(); document.activeElement?.blur?.()`);
  await key(page, 'ArrowRight');
  await waitFor(page, `${app}.graphDetailNode?.id === 'cap-auth-root'`, 'ArrowRight dispatches nodeSelect and updates graphDetailNode to P0 issue');
  await key(page, 'ArrowRight');
  await waitFor(page, `${app}.graphDetailNode && ${app}.graphDetailNode.id !== 'cap-auth-root' && ${app}.graphDetailNode.id === ${app}.forceGraphModule.getSelectedNode()?.id`, 'second ArrowRight advances graphDetailNode');
  await key(page, 'Escape');
  await waitFor(page, `!${app}.graphDetailNode`, 'Escape clears selected graph detail node');

  // [C-2] Closed-only historical cycles must NOT poison active WASM DiGraph in viewer.js or graph.js.
  const closedCycleIsolationAudit = await evaluate(page, `(async () => {
    const m = ${app}.forceGraphModule;
    DB_STATE.db.run("UPDATE issues SET status = 'closed' WHERE id IN ('cap-auth-root', 'cap-auth-ui', 'cap-billing-a', 'cap-billing-b')");
    DB_STATE.db.run("UPDATE issue_overview_mv SET status = 'closed', in_cycle = 0 WHERE id IN ('cap-auth-root', 'cap-auth-ui', 'cap-billing-a', 'cap-billing-b')");
    DB_STATE.db.run("INSERT INTO issues (id, title, status, priority, issue_type, labels, created_at, updated_at) VALUES ('cap-open-prereq', 'Active Open Prereq', 'open', 1, 'task', '[]', '2026-09-01T12:00:00Z', '2026-09-01T12:00:00Z')");
    DB_STATE.db.run("INSERT INTO issue_overview_mv (id, title, status, priority, issue_type, labels, created_at, updated_at, dependency_state, is_actionable, pagerank, betweenness, eigenvector, critical_path_depth, triage_score, blocks_count, blocked_by_count, blocker_count, dependent_count, critical_depth, in_cycle, comment_count) VALUES ('cap-open-prereq', 'Active Open Prereq', 'open', 1, 'task', '[]', '2026-09-01T12:00:00Z', '2026-09-01T12:00:00Z', 'satisfied', 1, 0.2, 0, 0, 2, 0.5, 1, 0, 0, 1, 2, 0, 0)");
    DB_STATE.db.run("INSERT INTO dependencies (issue_id, depends_on_id, type) VALUES ('cap-hub-leaf', 'cap-open-prereq', 'blocks')");
    try {
      cleanupWasm();
      await initGraphEngine();
      const gd = getGraphViewData();
      const subMetrics = recalculateMetrics(gd.issues.map(i => i.id));
      m.loadData(gd.issues, gd.dependencies, null);

      const topPaths = getTopCriticalPaths(5);
      m.animateCriticalPath(false);
      const cpState = m.getCriticalPathState();
      m.resetCriticalPath();
      const graphNodes = Object.fromEntries(m.getGraph().graphData().nodes.map(n => [n.id, { slack: n.slack, inCycle: n.inCycle, criticalDepth: n.criticalDepth }]));

      return {
        fullHasCycles: GRAPH_STATE.graph.hasCycles(),
        activeHasCycles: GRAPH_STATE.activeGraph.hasCycles(),
        recalcSubgraphHasCycles: subMetrics?.hasCycles,
        viewerCycleInfoHasCycles: getCycleInfo().hasCycles,
        viewerBreakSuggestionsLen: (getCycleBreakSuggestions(5)?.top_suggestions || getCycleBreakSuggestions(5)?.suggestions || []).length,
        viewerBreakTotalCycles: getCycleBreakSuggestions(5)?.total_cycles ?? -1,
        closedInCycle: isIssueInCycle('cap-auth-root'),
        zeroSlackIds: getIssuesBySlack(10, true).map(i => i.id).sort(),
        allSlackIds: getIssuesBySlack(10, false).map(i => i.id).sort(),
        topPath0Len: topPaths[0]?.length || 0,
        topPath0Hops: topPaths[0]?.hops ?? -1,
        topPath0Fmt: formatCriticalPathHops(topPaths[0]),
        forceFullHasCycles: m.getWasmGraph().hasCycles(),
        forceActiveHasCycles: m.getActiveWasmGraph().hasCycles(),
        forceMetricsHasCycles: m.getMetrics().hasCycles,
        cpStateActive: cpState.active,
        cpStatePathLen: cpState.path.length,
        graphNodes,
      };
    } finally {
      DB_STATE.db.run("DELETE FROM dependencies WHERE issue_id = 'cap-hub-leaf' AND depends_on_id = 'cap-open-prereq'");
      DB_STATE.db.run("DELETE FROM issue_overview_mv WHERE id = 'cap-open-prereq'");
      DB_STATE.db.run("DELETE FROM issues WHERE id = 'cap-open-prereq'");
      DB_STATE.db.run("UPDATE issues SET status = 'open' WHERE id IN ('cap-auth-root', 'cap-auth-ui', 'cap-billing-a', 'cap-billing-b')");
      DB_STATE.db.run("UPDATE issue_overview_mv SET status = 'open', in_cycle = 1 WHERE id IN ('cap-auth-root', 'cap-auth-ui', 'cap-billing-a', 'cap-billing-b')");
      cleanupWasm();
      await initGraphEngine();
      const origGd = getGraphViewData();
      recalculateMetrics(origGd.issues.map(i => i.id));
      m.loadData(origGd.issues, origGd.dependencies, null);
      try {
        const histResp = await fetch('./data/history.json');
        if (histResp.ok) {
          const histData = await histResp.json();
          if (histData?.commits?.length > 0 && m.initTimeTravel) m.initTimeTravel(histData);
        }
      } catch { /* optional */ }
    }
  })()`);
  assert.equal(closedCycleIsolationAudit.fullHasCycles, true, 'full historical WASM graph retains closed-only cycles');
  assert.equal(closedCycleIsolationAudit.activeHasCycles, false, 'active WASM graph excludes closed-only cycles');
  assert.equal(closedCycleIsolationAudit.recalcSubgraphHasCycles, false, 'recalculateMetrics uses activeGraph for subgraph cycle check');
  assert.equal(closedCycleIsolationAudit.viewerCycleInfoHasCycles, false, 'getCycleInfo ignores closed-only cycles');
  assert.equal(closedCycleIsolationAudit.viewerBreakSuggestionsLen, 0, 'getCycleBreakSuggestions ignores closed-only cycles');
  assert.equal(closedCycleIsolationAudit.viewerBreakTotalCycles, 0, 'cycleBreakSuggestions reports total_cycles=0 on active DAG');
  assert.equal(closedCycleIsolationAudit.closedInCycle, false, 'closed historical cycle issue is not reported as active cycle');
  assert.deepEqual(closedCycleIsolationAudit.zeroSlackIds, ['cap-hub-leaf', 'cap-open-prereq'], 'active critical chain has zero slack despite closed cycles');
  assert.deepEqual(closedCycleIsolationAudit.allSlackIds, ['cap-hub-leaf', 'cap-open-prereq'], 'getIssuesBySlack excludes all closed issues');
  assert.equal(closedCycleIsolationAudit.topPath0Len, 2, 'active 2-node chain found by kCriticalPaths despite closed cycles');
  assert.equal(closedCycleIsolationAudit.topPath0Hops, 1, '2-node critical path has 1 hop');
  assert.equal(closedCycleIsolationAudit.topPath0Fmt, '1 hop', '2-node critical path formats as singular 1 hop');
  assert.equal(closedCycleIsolationAudit.forceFullHasCycles, true, 'force-graph full WASM graph retains closed cycles');
  assert.equal(closedCycleIsolationAudit.forceActiveHasCycles, false, 'force-graph activeWasmGraph is acyclic');
  assert.equal(closedCycleIsolationAudit.forceMetricsHasCycles, false, 'force-graph metrics.hasCycles reflects activeWasmGraph');
  assert.equal(closedCycleIsolationAudit.cpStateActive, true, 'animateCriticalPath activates on active DAG despite closed cycles');
  assert.equal(closedCycleIsolationAudit.cpStatePathLen, 2, 'animateCriticalPath highlights 2-node active chain');
  assert.equal(closedCycleIsolationAudit.graphNodes['cap-hub-leaf'].slack, 0, 'active leaf gets 0 slack');
  assert.equal(closedCycleIsolationAudit.graphNodes['cap-open-prereq'].slack, 0, 'active prerequisite gets 0 slack');
  assert.equal(closedCycleIsolationAudit.graphNodes['cap-auth-root'].slack, null, 'closed historical cycle node gets null slack');
  assert.equal(closedCycleIsolationAudit.graphNodes['cap-auth-root'].inCycle, false, 'closed historical cycle node gets inCycle=false');

  // 4b. Scoped graph.js keyboard shortcuts (inactive outside #/graph) & >20-label top-20 cross-label heatmap matrix.
  await click(page, 'a[href="#/issues"]');
  await waitFor(page, `${app}.view === 'issues'`, 'returned to issues view after graph initialization');
  const scopedShortcutAndHeatmapAudit = await evaluate(page, `(() => {
    document.activeElement?.blur?.();
    const arrowEv = new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true, cancelable: true });
    document.body.dispatchEvent(arrowEv);
    const digit5Ev = new KeyboardEvent('keydown', { key: '5', code: 'Digit5', bubbles: true, cancelable: true });
    document.body.dispatchEvent(digit5Ev);

    const orig = getGraphViewData();
    const origResolved = getResolvedIssueIDs();
    try {
      const manyIssues = [];
      for (let i = 0; i < 23; i++) {
        const prefix = i < 20 ? 'a-idle-' + String(i).padStart(2, '0') : 'z-busy-' + String(i).padStart(2, '0');
        manyIssues.push({ id: 'hm-' + i, status: 'open', labels: JSON.stringify([prefix]), created_at: '2026-09-01T12:00:00Z', updated_at: '2026-09-01T12:00:00Z' });
      }
      // 25 distinct labels total: 20 idle 'a-idle-*', 3 'z-busy-*', plus 'z-busy-23' and 'z-busy-24' with cross-label blocking edges.
      manyIssues.push({ id: 'hm-23', status: 'open', labels: JSON.stringify(['z-busy-23']), created_at: '2026-09-01T12:00:00Z', updated_at: '2026-09-01T12:00:00Z' });
      manyIssues.push({ id: 'hm-24', status: 'open', labels: JSON.stringify(['z-busy-24']), created_at: '2026-09-01T12:00:00Z', updated_at: '2026-09-01T12:00:00Z' });
      const manyDeps = [
        { issue_id: 'hm-23', depends_on_id: 'hm-24', type: 'blocks' },
        { issue_id: 'hm-20', depends_on_id: 'hm-21', type: 'blocks' },
      ];
      window.bvCharts.init(manyIssues, manyDeps, new Set());
      const matrix25 = window.bvCharts.computeLabelMatrix();
      window.bvCharts.init(orig.issues, orig.dependencies, origResolved);
      const matrixOrig = window.bvCharts.computeLabelMatrix();
      return {
        arrowPrevented: arrowEv.defaultPrevented,
        galaxyHijackedOutsideGraph: ${app}.forceGraphModule.getLabelClusterState().active,
        matrix25Truncated: matrix25.truncated,
        matrix25Total: matrix25.totalLabels,
        matrix25Len: matrix25.labels.length,
        matrix25HasBusy: matrix25.labels.includes('z-busy-23') && matrix25.labels.includes('z-busy-24') && matrix25.labels.includes('z-busy-20') && matrix25.labels.includes('z-busy-21'),
        matrix25Max: matrix25.maxValue,
        matrixOrigTruncated: matrixOrig.truncated,
        matrixOrigLen: matrixOrig.labels.length,
      };
    } finally {
      window.bvCharts.init(orig.issues, orig.dependencies, origResolved);
    }
  })()`);
  assert.equal(scopedShortcutAndHeatmapAudit.arrowPrevented, false, 'graph.js does not preventDefault ArrowDown on #/issues');
  assert.equal(scopedShortcutAndHeatmapAudit.galaxyHijackedOutsideGraph, false, 'graph.js does not hijack Digit5 outside #/graph');
  assert.equal(scopedShortcutAndHeatmapAudit.matrix25Truncated, true, '>20 labels sets truncated=true');
  assert.equal(scopedShortcutAndHeatmapAudit.matrix25Total, 25, 'totalLabels reports all 25 distinct labels');
  assert.equal(scopedShortcutAndHeatmapAudit.matrix25Len, 20, 'heatmap caps matrix to top 20 labels');
  assert.equal(scopedShortcutAndHeatmapAudit.matrix25HasBusy, true, 'top-20 ranking retains z-busy-* labels with cross-label blocking edges over alphabetical a-idle-*');
  assert.ok(scopedShortcutAndHeatmapAudit.matrix25Max > 0, 'top-20 matrix preserves positive blocking cell counts');
  assert.equal(scopedShortcutAndHeatmapAudit.matrixOrigTruncated, false, '<=20 labels sets truncated=false');
  assert.equal(scopedShortcutAndHeatmapAudit.matrixOrigLen, 5, 'original 5-label capability dataset restored');

  // 4c. Wave 14: Hash routing (#/graph <-> #/), Force-Graph light mode, container-scoped legend/tooltip,
  // dependency_state blocked coloring, dependencyCone, Insights coverageSet + Tarjan SCC + mobile metric-tooltip + triageData sections,
  // corpus-global MAX(blocks_count) hybrid impact, and exact-ID substring search.
  await click(page, 'a[href="#/"]');
  await waitFor(page, `${app}.view === 'dashboard'`, 'navigated to dashboard for Hero CTA hash routing check');
  const dashboardTriageAudit = await evaluate(page, `(() => {
    const suffix = window.beadsViewer.getViewerAssetQuerySuffix();
    const resources = performance.getEntriesByType('resource').map(e => e.name);
    return {
      quickWinsLen: ${app}.getTriageQuickWins().length,
      blockersIds: ${app}.getTriageBlockersToClear().map(i => i.id).sort(),
      assetQuerySuffix: suffix,
      wasmResourceHasSuffix: Boolean(suffix) && resources.some(u => u.includes('bv_graph_bg.wasm' + suffix)),
    };
  })()`);
  assert.equal(dashboardTriageAudit.quickWinsLen, 0, 'capabilities-fixture has 0 claimable triage.json quick_wins because all open issues are blocked');
  assert.deepEqual(dashboardTriageAudit.blockersIds, ['cap-auth-root', 'cap-auth-ui', 'cap-billing-a', 'cap-billing-b'], 'triage.json blockers_to_clear populates all 4 cycle blockers');
  assert.match(dashboardTriageAudit.assetQuerySuffix, /^\?v=[A-Za-z0-9._-]+$/, 'viewer.js exposes ?v= cache-busting query suffix');
  assert.equal(dashboardTriageAudit.wasmResourceHasSuffix, true, 'bv_graph_bg.wasm fetched with ?v= cache-buster query suffix');

  await evaluate(page, `[...document.querySelectorAll('div')].find(el => el.getAttribute('@click') === "window.location.hash = '#/graph'").click()`);
  await waitFor(page, `window.location.hash === '#/graph' && ${app}.view === 'graph' && ${app}.forceGraphReady`, 'Hero Explore Graph CTA navigates via #/graph hash');
  await click(page, 'button span', 'Back to Dashboard');
  await waitFor(page, `window.location.hash === '#/' && ${app}.view === 'dashboard'`, 'Back to Dashboard resets hash to #/');
  await click(page, 'a[href="#/graph"]');
  await waitFor(page, `window.location.hash === '#/graph' && ${app}.view === 'graph' && ${app}.forceGraphReady`, 're-clicking top Graph nav fires hashchange cleanly');

  const wave14GraphAudit = await evaluate(page, `(() => {
    const m = ${app}.forceGraphModule;
    const initialMode = m.getGraphTheme();
    const initialBg = m.THEME.bg;
    const initialLink = m.THEME.link.default;
    ${app}.toggleDarkMode();
    const toggledMode = m.getGraphTheme();
    const toggledBg = m.THEME.bg;
    const toggledLink = m.THEME.link.default;
    const priorityP0AfterToggle = m.THEME.priority[0];
    const blockedColorInToggledMode = m.getNodeColor({ id: 'synth-dep-blocked', status: 'open', blocker_count: 0, dependency_state: 'blocked_open', priority: 2 });
    const expectedBlockedInToggled = m.THEME.status.blocked;
    const openSatisfiedColorInToggled = m.getNodeColor({ id: 'synth-dep-sat', status: 'open', blocker_count: 0, dependency_state: 'satisfied', priority: 2 });
    const expectedOpenInToggled = m.THEME.status.open;
    ${app}.toggleDarkMode();
    const restoredMode = m.getGraphTheme();
    const restoredBg = m.THEME.bg;

    const gdIssues = getGraphViewData().issues;
    const hasDepState = gdIssues.every(i => typeof i.dependency_state === 'string' && i.dependency_state.length > 0 && typeof i.is_actionable === 'boolean');

    m.setViewMode('galaxy');
    const legendInContainer = document.querySelector('#graph-container > .bv-label-legend');
    const legendInBody = document.querySelector('body > .bv-label-legend');
    const tooltipInContainer = document.querySelector('#graph-container > .bv-graph-tooltip');
    const tooltipInBody = document.querySelector('body > .bv-graph-tooltip');
    const legendStyle = legendInContainer ? getComputedStyle(legendInContainer) : null;
    const legendPos = legendStyle?.position || '';
    const legendZ = legendStyle?.zIndex || '';
    m.setViewMode('force');

    const rootNode = m.getGraph().graphData().nodes.find(n => n.id === 'cap-auth-root');
    m.selectNode(rootNode);

    return {
      initialMode,
      initialBg,
      initialLink,
      toggledMode,
      toggledBg,
      toggledLink,
      restoredMode,
      restoredBg,
      priorityP0AfterToggle,
      blockedColorInToggledMode,
      expectedBlockedInToggled,
      openSatisfiedColorInToggled,
      expectedOpenInToggled,
      hasDepState,
      legendScoped: Boolean(legendInContainer) && !legendInBody,
      tooltipScoped: !tooltipInBody && Boolean(tooltipInContainer || true),
      legendPos,
      legendZ,
    };
  })()`);
  assert.ok((wave14GraphAudit.initialMode === 'dark' || wave14GraphAudit.initialMode === 'light') && wave14GraphAudit.initialMode !== wave14GraphAudit.toggledMode,
    `toggleDarkMode switches graph.js theme palette: ${JSON.stringify(wave14GraphAudit)}`);
  assert.notEqual(wave14GraphAudit.initialBg, wave14GraphAudit.toggledBg, 'toggleDarkMode switches THEME.bg between dark and light palettes');
  assert.notEqual(wave14GraphAudit.initialLink, wave14GraphAudit.toggledLink, 'toggleDarkMode switches THEME.link.default between dark and light palettes');
  assert.equal(wave14GraphAudit.restoredMode, wave14GraphAudit.initialMode, 'second toggleDarkMode restores original graph theme');
  assert.equal(wave14GraphAudit.restoredBg, wave14GraphAudit.initialBg, 'second toggleDarkMode restores original THEME.bg');
  assert.equal(wave14GraphAudit.priorityP0AfterToggle, '#FF0000', 'THEME.priority[0] stays invariant across graph theme switches');
  assert.equal(wave14GraphAudit.blockedColorInToggledMode, wave14GraphAudit.expectedBlockedInToggled, 'open node with non-satisfied dependency_state colors as blocked even if blocker_count=0');
  assert.equal(wave14GraphAudit.openSatisfiedColorInToggled, wave14GraphAudit.expectedOpenInToggled, 'open satisfied node colors as open');
  assert.equal(wave14GraphAudit.hasDepState, true, 'getGraphViewData includes dependency_state and is_actionable');
  assert.equal(wave14GraphAudit.legendScoped, true, '.bv-label-legend mounts inside #graph-container and never leaks onto document.body');
  assert.equal(wave14GraphAudit.tooltipScoped, true, '.bv-graph-tooltip never mounts onto document.body');
  assert.equal(wave14GraphAudit.legendPos, 'absolute', '.bv-label-legend uses position: absolute inside #graph-container');
  assert.equal(wave14GraphAudit.legendZ, '25', '.bv-label-legend uses scoped z-index: 25');

  await waitFor(page, `${app}.graphDetailNode?.id === 'cap-auth-root'`, 'cap-auth-root selected for modal-open & Focus Cone checks');
  assert.equal(await evaluate(page, `document.body.classList.contains('modal-open')`), true, 'selecting graph node sets body.modal-open');
  // [C-3] Leaving #/graph while graphDetailNode is open must clear graphDetailNode & body.modal-open!
  await click(page, 'button span', 'Back to Dashboard');
  await waitFor(page, `window.location.hash === '#/' && ${app}.view === 'dashboard'`, 'Back to Dashboard from open graphDetailNode');
  assert.equal(await evaluate(page, `document.body.classList.contains('modal-open')`), false, 'leaving #/graph clears body.modal-open');
  assert.equal(await evaluate(page, `${app}.graphDetailNode`), null, 'leaving #/graph resets graphDetailNode to null');
  await click(page, 'a[href="#/graph"]');
  await waitFor(page, `window.location.hash === '#/graph' && ${app}.view === 'graph' && ${app}.forceGraphReady`, 'returned to #/graph for Focus Cone checks');
  await evaluate(page, `(() => {
    const m = ${app}.forceGraphModule;
    const rootNode = m.getGraph().graphData().nodes.find(n => n.id === 'cap-auth-root');
    m.selectNode(rootNode);
  })()`);
  await waitFor(page, `${app}.graphDetailNode?.id === 'cap-auth-root'`, 're-selected cap-auth-root for Focus Cone check');

  await click(page, '#graph-focus-cone-btn');
  await waitFor(page, `${app}.dependencyConeActive === true && ${app}.dependencyConeFocusId === 'cap-auth-root'`, 'Focus Cone button isolates transitive dependency cone');
  const coneState = await evaluate(page, `({
    count: ${app}.dependencyConeCount,
    hudVisible: (${visible})(document.getElementById('graph-dependency-cone-hud')),
    coneIds: ${app}.forceGraphModule.getDependencyConeState().nodeIds.sort(),
  })`);
  assert.ok(coneState.count >= 3 && coneState.hudVisible, `dependencyCone highlights transitive cone and HUD: ${JSON.stringify(coneState)}`);
  assert.deepEqual(coneState.coneIds, ['cap-auth-root', 'cap-auth-ui', 'cap-hub-leaf'], 'cap-auth-root dependency cone isolates auth cycle + downstream telemetry hub');

  // [C-6] Selecting another node, clearSelection, performWhatIf, and toggleCycleNavigator must each clear dependencyCone!
  const coneResetAudit = await evaluate(page, `(() => {
    const m = ${app}.forceGraphModule;
    const nodes = m.getGraph().graphData().nodes;
    const rootNode = nodes.find(n => n.id === 'cap-auth-root');
    const uiNode = nodes.find(n => n.id === 'cap-auth-ui');

    m.selectNode(uiNode);
    const clearedBySelect = !m.getDependencyConeState().active && !${app}.dependencyConeActive && !rootNode._coneFocus;

    m.focusDependencyCone('cap-auth-ui', false);
    const activeBeforeClear = m.getDependencyConeState().active;
    m.clearSelection();
    const clearedByClearSelection = activeBeforeClear && !m.getDependencyConeState().active && !${app}.dependencyConeActive && !uiNode._coneFocus;

    m.focusDependencyCone('cap-auth-root', false);
    m.performWhatIf(rootNode);
    const clearedByWhatIf = !m.getDependencyConeState().active && !${app}.dependencyConeActive;
    m.resetWhatIf();

    m.focusDependencyCone('cap-auth-root', false);
    m.toggleCycleNavigator();
    const clearedByCycleNav = !m.getDependencyConeState().active && !${app}.dependencyConeActive;
    m.resetCycleNavigator();

    return { clearedBySelect, clearedByClearSelection, clearedByWhatIf, clearedByCycleNav };
  })()`);
  assert.deepEqual(coneResetAudit, {
    clearedBySelect: true,
    clearedByClearSelection: true,
    clearedByWhatIf: true,
    clearedByCycleNav: true,
  }, 'selectNode, clearSelection, performWhatIf, and toggleCycleNavigator all clear dependencyCone cleanly');

  await evaluate(page, `(() => {
    const m = ${app}.forceGraphModule;
    m.selectNode(m.getGraph().graphData().nodes.find(n => n.id === 'cap-auth-root'));
    m.focusDependencyCone('cap-auth-root', false);
  })()`);
  await key(page, 'Escape');
  await waitFor(page, `${app}.dependencyConeActive === false && ${app}.graphDetailNode?.id === 'cap-auth-root'`, 'first Escape clears dependency cone while preserving graphDetailNode');
  await key(page, 'Escape');
  await waitFor(page, `!${app}.graphDetailNode`, 'second Escape clears graphDetailNode and selection');

  await click(page, 'a[href="#/insights"]');
  await waitFor(page, `${app}.view === 'insights'`, 'navigated to insights for Wave 14/15 WASM & triage checks');
  const insightsWave14Audit = await evaluate(page, `(async () => {
    const cov = ${app}.coverageSet;
    const cyc = ${app}.cycleInfo;
    const covCardVisible = (${visible})(document.getElementById('dependency-coverage-set-card'));

    // [C-6] getCoverageSet() must NEVER fall back to closed GRAPH_STATE.graph when activeGraph has 0 edges
    const savedActiveGraph = GRAPH_STATE.activeGraph;
    const emptyActive = new window.bvGraphWasm.DiGraph();
    emptyActive.addNode('isolated-active');
    GRAPH_STATE.activeGraph = emptyActive;
    const emptyCov = getCoverageSet(10);
    GRAPH_STATE.activeGraph = savedActiveGraph;
    emptyActive.free();

    // [C-5] 1-node self-loop in WASM DiGraph + max_cycles=0 truncated semantics
    const selfG = new window.bvGraphWasm.DiGraph();
    const uSelf = selfG.addNode('self-1');
    selfG.addEdge(uSelf, uSelf);
    const selfHasCycles = selfG.hasCycles();
    const selfTarjan = selfG.tarjanScc();
    const selfEnum5 = selfG.enumerateCycles(5);
    const selfEnum5Ids = (selfEnum5?.cycles || []).map(c => c.map(idx => selfG.nodeId(idx)));
    const selfEnum0 = selfG.enumerateCycles(0);
    const selfSuggest = selfG.cycleBreakSuggestions(5, 20);
    selfG.free();

    const dagG = new window.bvGraphWasm.DiGraph();
    const dA = dagG.addNode('dag-a');
    const dB = dagG.addNode('dag-b');
    dagG.addEdge(dA, dB);
    const dagEnum0 = dagG.enumerateCycles(0);
    dagG.free();

    // Mobile/click .metric-tooltip toggle inside Critical Path Slack
    const trigger = document.querySelector('.metric-tooltip-trigger');
    const tip = trigger?.querySelector('.metric-tooltip');
    trigger?.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }));
    const tipOpened = Boolean(tip?.classList.contains('tooltip-visible'));
    document.body.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }));
    const tipClosedOnOutsideClick = !tip?.classList.contains('tooltip-visible');

    // [C-2] capabilities-fixture has 0 actionable items -> triage.json must NOT run unscoped fallback!
    const byTrackLen = ${app}.getTriageRecommendationsByTrack().length;
    const byLabelLen = ${app}.getTriageRecommendationsByLabel().length;

    const origRecs = ${app}.triageData?.recommendations;
    const origByTrack = ${app}.triageData?.recommendations_by_track;
    const origByLabel = ${app}.triageData?.recommendations_by_label;
    const origAuth = ${app}.triageData?.source_authority;
    let copiedCmd = '';
    let tabAfterTrack = '';
    let tabAfterLabel = '';
    let bannerShownWhenProvisional = false;
    let claimHiddenWhenProvisional = false;
    let showKeptWhenProvisional = false;
    let helperOmitsClaimWhenProvisional = false;
    if (${app}.triageData) {
      ${app}.triageData.recommendations = [{
        id: 'cap-auth-root',
        title: 'OAuth login gateway',
        score: 0.91,
        priority: 0,
        status: 'open',
        actions: {
          claim: { shell: 'br update cap-auth-root --status in_progress' },
          show: { shell: 'br show cap-auth-root --json' },
        },
      }];
      ${app}.triageData.recommendations_by_track = [{
        track_id: 'track-A',
        reason: 'Auth cycle track',
        claim_command: 'br update cap-auth-root --status in_progress',
        top_pick: { id: 'cap-auth-root', title: 'OAuth login gateway', score: 0.91, priority: 0 },
      }];
      ${app}.triageData.recommendations_by_label = [{
        label: 'backend',
        claim_command: 'br update cap-auth-root --status in_progress',
        top_pick: { id: 'cap-auth-root', title: 'OAuth login gateway', score: 0.91, priority: 0 },
      }];
      await new Promise(r => setTimeout(r, 30));

      document.querySelector('[data-rec-tab="track"]')?.click();
      tabAfterTrack = ${app}.triageRecView;
      document.querySelector('[data-rec-tab="label"]')?.click();
      tabAfterLabel = ${app}.triageRecView;
      document.querySelector('[data-rec-tab="top"]')?.click();
      await new Promise(r => setTimeout(r, 30));

      const cmdBtn = document.querySelector('[data-rec-claim-cmd]');
      cmdBtn?.click();
      await new Promise(r => setTimeout(r, 30));
      copiedCmd = ${app}.copiedCommandText || '';

      // Provisional source_authority warning banner + Claim button suppression check
      ${app}.triageData.source_authority = { state: 'provisional_jsonl', claim_safe: false };
      await new Promise(r => setTimeout(r, 30));
      bannerShownWhenProvisional = (${visible})(document.querySelector('[data-source-authority-banner="insights"]'));
      claimHiddenWhenProvisional = !(${visible})(document.querySelector('[data-rec-claim-cmd]'));
      showKeptWhenProvisional = (${visible})(document.querySelector('[data-rec-show-cmd]'));
      const cmdsUnderProvisional = ${app}.getIssueActionCommands('cap-auth-root');
      helperOmitsClaimWhenProvisional = !cmdsUnderProvisional?.claim && Boolean(cmdsUnderProvisional?.show?.shell);

      ${app}.triageData.recommendations = origRecs;
      ${app}.triageData.recommendations_by_track = origByTrack;
      ${app}.triageData.recommendations_by_label = origByLabel;
      ${app}.triageData.source_authority = origAuth;
      await new Promise(r => setTimeout(r, 30));
    }

    return {
      covTotalEdges: cov?.total_edges || 0,
      covRatio: cov?.coverage_ratio || 0,
      covItemsLen: cov?.items?.length || 0,
      covCardVisible,
      emptyCov,
      selfLoop: {
        hasCycles: selfHasCycles,
        cycleCount: selfTarjan?.cycle_count,
        cycles5: selfEnum5Ids,
        trunc5: selfEnum5?.truncated,
        trunc0: selfEnum0?.truncated,
        firstSuggestion: selfSuggest?.suggestions?.[0] ? [selfSuggest.suggestions[0].from_id, selfSuggest.suggestions[0].to_id] : null,
        dagTrunc0: dagEnum0?.truncated,
      },
      sccCount: cyc?.sccCount || 0,
      nonTrivialSccCount: cyc?.nonTrivialSccCount || 0,
      largestSccSize: cyc?.largestSccSize || 0,
      cycTruncated: cyc?.truncated,
      tipOpened,
      tipClosedOnOutsideClick,
      byTrackLen,
      byLabelLen,
      copiedCmd,
      tabAfterTrack,
      tabAfterLabel,
      bannerShownWhenProvisional,
      claimHiddenWhenProvisional,
      showKeptWhenProvisional,
      helperOmitsClaimWhenProvisional,
    };
  })()`);
  assert.ok(insightsWave14Audit.covTotalEdges > 0 && insightsWave14Audit.covRatio > 0 && insightsWave14Audit.covItemsLen > 0 && insightsWave14Audit.covCardVisible,
    `Dependency Coverage Set card renders WASM coverageSetDefault: ${JSON.stringify(insightsWave14Audit)}`);
  assert.deepEqual(
    [insightsWave14Audit.emptyCov.items.length, insightsWave14Audit.emptyCov.edgesCovered, insightsWave14Audit.emptyCov.totalEdges, insightsWave14Audit.emptyCov.coverageRatio],
    [0, 0, 0, 1],
    'getCoverageSet() never falls back to closed GRAPH_STATE.graph when activeGraph has 0 edges'
  );
  assert.deepEqual(insightsWave14Audit.selfLoop, {
    hasCycles: true,
    cycleCount: 1,
    cycles5: [['self-1']],
    trunc5: false,
    trunc0: true,
    firstSuggestion: ['self-1', 'self-1'],
    dagTrunc0: false,
  }, 'WASM DiGraph detects 1-node self-loop cycle, suggests self-edge break, and sets truncated accurately for max_cycles=0');
  assert.equal(insightsWave14Audit.nonTrivialSccCount, 2, 'Tarjan SCC reports 2 non-trivial active cycle SCCs');
  assert.equal(insightsWave14Audit.largestSccSize, 2, 'Tarjan SCC reports largest SCC size = 2');
  assert.equal(insightsWave14Audit.cycTruncated, false, 'exact cycle count <= max_cycles sets truncated=false');
  assert.equal(insightsWave14Audit.tipOpened, true, 'clicking .metric-tooltip-trigger toggles .tooltip-visible for coarse/touch pointers');
  assert.equal(insightsWave14Audit.tipClosedOnOutsideClick, true, 'outside click closes .metric-tooltip');
  assert.equal(insightsWave14Audit.byTrackLen, 0, '0-actionable capabilities-fixture does not leak unscoped By Track fallback');
  assert.equal(insightsWave14Audit.byLabelLen, 0, '0-actionable capabilities-fixture does not leak unscoped By Label fallback');
  assert.equal(insightsWave14Audit.tabAfterTrack, 'track');
  assert.equal(insightsWave14Audit.tabAfterLabel, 'label');
  assert.ok(insightsWave14Audit.copiedCmd.includes('br '), `clicking recommendation command badge copies shell command: ${insightsWave14Audit.copiedCmd}`);
  assert.equal(insightsWave14Audit.bannerShownWhenProvisional, true, 'provisional source_authority (claim_safe=false) displays warning banner');
  assert.equal(insightsWave14Audit.claimHiddenWhenProvisional, true, 'provisional source_authority hides Claim button');
  assert.equal(insightsWave14Audit.showKeptWhenProvisional, true, 'provisional source_authority keeps Show button');
  assert.equal(insightsWave14Audit.helperOmitsClaimWhenProvisional, true, 'getIssueActionCommands omits claim when hasProvisionalSourceAuthority()');

  // Wave 25: Hygiene Suggestions card in #/insights + absent-sprints meta gating (zero 404s)
  const wave25InsightsCheck = await evaluate(page, `(() => {
    const sugCard = document.getElementById('insights-hygiene-suggestions');
    const sugVisible = (${visible})(sugCard);
    const sugCount = ${app}.suggestionsData?.suggestions?.length || 0;
    const cycleCount = ${app}.suggestionTypeCounts?.cycle_warning || 0;
    const prevFilter = ${app}.suggestionsTypeFilter;
    ${app}.suggestionsTypeFilter = 'cycle_warning';
    const filteredCycleLen = ${app}.filteredSuggestions.length;
    ${app}.suggestionsTypeFilter = prevFilter;
    return {
      hasSuggestions: ${app}.hasSuggestions,
      hasSprints: ${app}.hasSprints,
      sprintsNavVisible: (${visible})(document.getElementById('nav-sprints-link')),
      sugVisible,
      sugCount,
      cycleCount,
      filteredCycleLen,
    };
  })()`);
  assert.equal(wave25InsightsCheck.hasSuggestions, true, 'capabilities-fixture emits hygiene suggestions (cycle_warning)');
  assert.equal(wave25InsightsCheck.sugVisible, true, '#insights-hygiene-suggestions card is visible');
  assert.ok(wave25InsightsCheck.sugCount > 0 && wave25InsightsCheck.cycleCount > 0 && wave25InsightsCheck.filteredCycleLen === wave25InsightsCheck.cycleCount,
    `Hygiene suggestions type filter works accurately: ${JSON.stringify(wave25InsightsCheck)}`);
  assert.equal(wave25InsightsCheck.hasSprints, false, 'capabilities-fixture without sprints.jsonl keeps hasSprints=false');
  assert.equal(wave25InsightsCheck.sprintsNavVisible, false, 'Sprints nav link hidden when hasSprints=false');

  // Wave 26: Label Intelligence & Cross-Label Flow (data/labels.json) + Global Backlog Capacity & Forecast (data/forecast.json) + B9-2 UTC date edge cases
  const wave26InsightsCheck = await evaluate(page, `(() => {
    const lblCard = document.getElementById('insights-label-intelligence');
    const fcCard = document.getElementById('insights-forecast-suite');
    const sortedLbls = ${app}.getSortedLabelIntelligence();
    const flowEdges = ${app}.getCrossLabelFlowEdges();
    return {
      hasLabelsMeta: ${app}.meta?.has_labels === true || ${app}.meta?.has_labels === 'true',
      hasLabels: ${app}.hasLabels,
      lblCardVisible: Boolean(lblCard && (${visible})(lblCard)),
      labelCount: sortedLbls.length,
      flowEdgeCount: flowEdges.length,
      hasForecastMeta: ${app}.meta?.has_forecast === true || ${app}.meta?.has_forecast === 'true',
      hasForecast: ${app}.hasForecast,
      fcCardVisible: Boolean(fcCard && (${visible})(fcCard)),
      capOpenCount: ${app}.forecastData?.global_capacity?.open_count || 0,
      capDays1a: Number(${app}.forecastData?.global_capacity?.days_1_agent || 0),
      // B9-2 & B10-P2-7 & B11-5: non-UTC fixed offset ISO date preserves literal calendar day; zero/impossible/malformed times return ''
      offsetDatePreserved: formatCalendarDateUTC('2026-03-08T00:00:00+02:00') === 'Mar 8, 2026',
      zeroDateBlank: formatCalendarDateUTC('0001-01-01T00:00:00Z') === '',
      impossibleDateBlank: formatCalendarDateUTC('2026-02-30') === '' && formatCalendarDateUTC('2026-13-01') === '' && formatCalendarDateUTC('2026-03-08foo') === '' && formatCalendarDateUTC('2026-03-08T99:99:99Z') === '' && formatCalendarDateUTC('2026-03-08 garbage') === '',
      labelSortAriaPressedOk: document.querySelectorAll('#insights-label-intelligence [role="group"] button[aria-pressed="true"]').length === 1,
      sparklineWorks: ${app}.formatVelocitySparkline([0, 1, 3, 2]).length === 4 && ${app}.formatVelocitySparkline([0, 0, 0, 0]) === '',
      trendArrowWorks: ${app}.formatLabelTrend('accelerating') === '↗' && ${app}.formatLabelTrend('decelerating') === '↘',
      labelsHaveWeeklyClosed: sortedLbls.some(l => Array.isArray(l.weekly_closed) && l.weekly_closed.length === 4),
    };
  })()`);
  assert.equal(wave26InsightsCheck.hasLabelsMeta, true, 'Wave 26 meta.has_labels=true');
  assert.equal(wave26InsightsCheck.hasLabels, true, 'Wave 26 hasLabels=true');
  assert.equal(wave26InsightsCheck.lblCardVisible, true, 'Wave 26 #insights-label-intelligence visible');
  assert.ok(wave26InsightsCheck.labelCount >= 2, `Wave 26 label intelligence rows >= 2: ${JSON.stringify(wave26InsightsCheck)}`);
  assert.equal(wave26InsightsCheck.hasForecastMeta, true, 'Wave 26 meta.has_forecast=true');
  assert.equal(wave26InsightsCheck.hasForecast, true, 'Wave 26 hasForecast=true');
  assert.equal(wave26InsightsCheck.fcCardVisible, true, 'Wave 26 #insights-forecast-suite visible even without sprints.jsonl');
  assert.ok(wave26InsightsCheck.capOpenCount > 0 && wave26InsightsCheck.capDays1a > 0, `Wave 26 global capacity populated: ${JSON.stringify(wave26InsightsCheck)}`);
  assert.equal(wave26InsightsCheck.offsetDatePreserved, true, 'B9-2 non-UTC fixed offset ISO date preserves literal calendar day');
  assert.equal(wave26InsightsCheck.zeroDateBlank, true, 'B9-2 Go zero time formats as empty string');
  assert.equal(wave26InsightsCheck.impossibleDateBlank, true, 'B10-P2-7 & B11-5 impossible/malformed ISO dates and invalid time suffixes reject and return empty string');
  assert.equal(wave26InsightsCheck.labelSortAriaPressedOk, true, 'B11-5 #insights-label-intelligence sort buttons expose role=group + aria-pressed');
  assert.equal(wave26InsightsCheck.sparklineWorks, true, 'W27-4 formatVelocitySparkline renders 4-week mini-bars and hides all-zero');
  assert.equal(wave26InsightsCheck.trendArrowWorks, true, 'W27-4 formatLabelTrend maps accelerating/decelerating arrows');
  assert.equal(wave26InsightsCheck.labelsHaveWeeklyClosed, true, 'W27-4 data/labels.json exports 4-week chronological weekly_closed arrays');

  // Wave 25: Issues quick filters (#filter-in-cycle-btn, #filter-articulation-btn), new sorts (pagerank/betweenness/due/estimate), and Detail Modal K-Core/Slack/Correlated Commits
  await click(page, 'a[href="#/issues"]');
  await waitFor(page, `${app}.view === 'issues'`, 'navigated to issues for Wave 25 quick filter and structural metric check');
  await click(page, '#filter-in-cycle-btn');
  await waitFor(page, `${app}.filters.inCycle === true && window.location.hash.includes('cycle=true')`, 'In Cycle quick filter updates state and URL');
  const inCycleIds = await evaluate(page, `${app}.issues.map(i => i.id).sort()`);
  assert.deepEqual(inCycleIds, ['cap-auth-root', 'cap-auth-ui', 'cap-billing-a', 'cap-billing-b'], 'In Cycle filter returns exact 4 active cycle issues');
  await click(page, '#filter-in-cycle-btn');
  await waitFor(page, `${app}.filters.inCycle === false`, 'toggling In Cycle quick filter off clears filter');

  await click(page, '#filter-articulation-btn');
  await waitFor(page, `${app}.filters.articulation === true && window.location.hash.includes('cutvertex=true')`, 'Cut Vertex quick filter updates state and URL');
  const artIds = await evaluate(page, `${app}.issues.map(i => i.id).sort()`);
  assert.deepEqual(artIds, ['cap-auth-root'], 'Cut Vertex filter returns active articulation point cap-auth-root');
  await click(page, '#filter-articulation-btn');
  await waitFor(page, `${app}.filters.articulation === false`, 'toggling Cut Vertex quick filter off clears filter');

  const wave25DetailMetrics = await evaluate(page, `(() => {
    const sel = document.getElementById('issues-sort-select');
    const sortVals = [...sel.options].map(o => o.value);
    const prIssues = queryIssues({}, 'pagerank', 10, 0).map(i => i.id);
    const bwIssues = queryIssues({}, 'betweenness', 10, 0).map(i => i.id);
    const dueIssues = queryIssues({}, 'due', 10, 0).map(i => i.id);
    const issue = getIssue('cap-auth-root');
    const closedIssue = getIssue('cap-closed-prereq');
    const commits = ${app}.getIssueCommits('cap-auth-root');
    return {
      hasNewSorts: ['pagerank', 'betweenness', 'due', 'estimate', 'eta'].every(v => sortVals.includes(v)),
      sortsReturnAll6: prIssues.length === 6 && bwIssues.length === 6 && dueIssues.length === 6 && queryIssues({}, 'eta', 10, 0).length === 6,
      kcoreType: typeof issue?.kcore,
      isCutVertex: issue?.is_articulation_point === true,
      closedSuppressesCutVertex: closedIssue?.is_articulation_point === false && closedIssue?.kcore === null,
      hasHistoryMeta: ${app}.meta?.has_history === true || ${app}.meta?.has_history === 'true',
      commitsLen: Array.isArray(commits) ? commits.length : -1,
      firstCommitHasShaAndMsg: Boolean(commits?.[0]?.short_sha && commits?.[0]?.message),
      openHasETAPill: Boolean(${app}.formatETAPill(issue)),
      closedSuppressesETAPill: ${app}.formatETAPill(closedIssue) === '',
      rootChainOnBlocked: Boolean(computeRootBlockerChain('cap-auth-ui')?.isBlocked),
      rootChainSuppressedOnClosed: computeRootBlockerChain('cap-closed-prereq') === null,
    };
  })()`);
  assert.deepEqual(wave25DetailMetrics, {
    hasNewSorts: true,
    sortsReturnAll6: true,
    kcoreType: 'number',
    isCutVertex: true,
    closedSuppressesCutVertex: true,
    hasHistoryMeta: true,
    commitsLen: wave25DetailMetrics.commitsLen >= 1 ? wave25DetailMetrics.commitsLen : 1,
    firstCommitHasShaAndMsg: true,
    openHasETAPill: true,
    closedSuppressesETAPill: true,
    rootChainOnBlocked: true,
    rootChainSuppressedOnClosed: true,
  }, `Issues new sorts (incl. eta), active-only structural metrics, ETA pills, root blocker chain, and non-empty correlated commits verified: ${JSON.stringify(wave25DetailMetrics)}`);

  // Wave 27 B11-1 + B11-2 + W27-2: fast-path & fallback parity on external/missing blockers and self-loops + Graph Detail Drawer ETA/Root Blockers/Commits
  const wave27RootAndGraphAudit = await evaluate(page, `(() => {
    const savedModel = GRAPH_STATE.whatIfModel;
    // B11-2: self-loop on fallback path (when whatIfModel is null)
    DB_STATE.db.run("INSERT INTO issues (id, title, status, priority, issue_type, labels, created_at, updated_at) VALUES ('cap-self-loop', 'Self loop probe', 'open', 1, 'task', '[]', '2026-09-01T12:00:00Z', '2026-09-01T12:00:00Z')");
    DB_STATE.db.run("INSERT INTO issue_overview_mv (id, title, status, priority, issue_type, labels, created_at, updated_at, dependency_state, is_actionable, pagerank, betweenness, eigenvector, critical_path_depth, triage_score, blocks_count, blocked_by_count, blocker_count, dependent_count, critical_depth, in_cycle, comment_count) VALUES ('cap-self-loop', 'Self loop probe', 'open', 1, 'task', '[]', '2026-09-01T12:00:00Z', '2026-09-01T12:00:00Z', 'blocked', 0, 0, 0, 0, 0, 0.1, 1, 1, 1, 1, 0, 1, 0)");
    DB_STATE.db.run("INSERT INTO dependencies (issue_id, depends_on_id, type) VALUES ('cap-self-loop', 'cap-self-loop', 'blocks')");
    DB_STATE.db.run("INSERT INTO issues (id, title, status, priority, issue_type, labels, created_at, updated_at) VALUES ('cap-ext-only', 'Missing blocker only', 'open', 1, 'task', '[]', '2026-09-01T12:00:00Z', '2026-09-01T12:00:00Z')");
    DB_STATE.db.run("INSERT INTO issue_overview_mv (id, title, status, priority, issue_type, labels, created_at, updated_at, dependency_state, is_actionable, pagerank, betweenness, eigenvector, critical_path_depth, triage_score, blocks_count, blocked_by_count, blocker_count, dependent_count, critical_depth, in_cycle, comment_count) VALUES ('cap-ext-only', 'Missing blocker only', 'open', 1, 'task', '[]', '2026-09-01T12:00:00Z', '2026-09-01T12:00:00Z', 'blocked', 0, 0, 0, 0, 0, 0.1, 0, 1, 1, 0, 0, 0, 0)");
    DB_STATE.db.run("INSERT INTO dependencies (issue_id, depends_on_id, type) VALUES ('cap-ext-only', 'cap-nonexistent-ext', 'blocks')");
    try {
      GRAPH_STATE.whatIfModel = null;
      const fallbackSelf = computeRootBlockerChain('cap-self-loop');
      const fallbackExtOnly = computeRootBlockerChain('cap-ext-only');
      return {
        fallbackSelfHasCycle: Boolean(fallbackSelf?.isBlocked && fallbackSelf?.hasCycle && fallbackSelf?.chainLength === 0),
        fallbackExtOnlyNull: fallbackExtOnly === null,
        graphDataHasETA: getGraphViewData().issues.some(i => i.id === 'cap-auth-root' && i.eta_days != null && Boolean(i.eta_date)),
      };
    } finally {
      GRAPH_STATE.whatIfModel = savedModel;
      DB_STATE.db.run("DELETE FROM dependencies WHERE issue_id IN ('cap-self-loop', 'cap-ext-only')");
      DB_STATE.db.run("DELETE FROM issue_overview_mv WHERE id IN ('cap-self-loop', 'cap-ext-only')");
      DB_STATE.db.run("DELETE FROM issues WHERE id IN ('cap-self-loop', 'cap-ext-only')");
    }
  })()`);
  assert.deepEqual(wave27RootAndGraphAudit, {
    fallbackSelfHasCycle: true,
    fallbackExtOnlyNull: true,
    graphDataHasETA: true,
  }, `Wave 27 B11-1/B11-2/W27-2 root-blocker self-loop, missing-blocker, and graph ETA data verified: ${JSON.stringify(wave27RootAndGraphAudit)}`);

  // Corpus-global MAX(blocks_count) hybrid impact normalization, [C-1] template/pinned hybrid status parity, & exact-ID substring search.
  await click(page, 'a[href="#/issues"]');
  await waitFor(page, `${app}.view === 'issues'`, 'returned to issues for Wave 14/15 hybrid impact & exact-ID search check');
  await evaluate(page, `(() => {
    const sel = [...document.querySelectorAll('select[x-model="searchMode"]')].find(${visible});
    sel.value = 'hybrid';
    sel.dispatchEvent(new Event('change', { bubbles: true }));
  })()`);
  await search(page, 'zircon-design-arch-v2');
  await resultIDs(page, ['cap-auth-ui']);
  const globalImpactRatio = await evaluate(page, `${app}.issues[0]?.component_scores?.impact`);
  assert.ok(Math.abs(globalImpactRatio - 0.5) < 1e-9, `single-result hybrid search normalizes blocks_count=1 against corpus MAX(blocks_count)=2 -> 0.5, got ${globalImpactRatio}`);
  const hybridStatusParityAudit = await evaluate(page, `(() => {
    const weights = typeof HYBRID_PRESETS !== 'undefined' ? HYBRID_PRESETS.default : { text: 0.4, pagerank: 0.2, status: 0.15, impact: 0.1, priority: 0.1, recency: 0.05 };
    const rows = [
      { id: 'tmpl-row', textScore: 0.5, pagerank: 0.5, status: 'open', is_template: 1, blockerCount: 1, priority: 1, updatedAt: '2026-09-04T12:00:00Z' },
      { id: 'pinned-flag-row', textScore: 0.5, pagerank: 0.5, status: 'open', pinned: 1, blockerCount: 1, priority: 1, updatedAt: '2026-09-04T12:00:00Z' },
      { id: 'pinned-status-row', textScore: 0.5, pagerank: 0.5, status: 'pinned', blockerCount: 1, priority: 1, updatedAt: '2026-09-04T12:00:00Z' },
      { id: 'future-defer-row', textScore: 0.5, pagerank: 0.5, status: 'open', defer_until: '2099-01-01T00:00:00Z', blockerCount: 1, priority: 1, updatedAt: '2026-09-04T12:00:00Z' },
    ];
    const rankedJs = new HybridScorer(weights, { maxBlockerCount: 4 }).scoreAndRank(rows);
    const rankedBatch = window.scoreBatchHybrid(rows, weights, { maxBlockerCount: 4 });
    const mvCols = execQuery("PRAGMA table_info(issue_overview_mv)").map(c => c.name);
    return {
      jsStatuses: rankedJs.map(r => r.component_scores.status),
      batchStatuses: rankedBatch.map(r => r.component_scores.status),
      batchImpacts: rankedBatch.map(r => r.component_scores.impact),
      hasTemplateCol: mvCols.includes('is_template'),
      hasPinnedCol: mvCols.includes('pinned'),
    };
  })()`);
  assert.deepEqual(hybridStatusParityAudit.jsStatuses, [0.2, 0.2, 0.2, 0.2], 'HybridScorer normalizes is_template, pinned, status=pinned, and future defer_until to 0.2');
  assert.deepEqual(hybridStatusParityAudit.batchStatuses, [0.2, 0.2, 0.2, 0.2], 'scoreBatchHybrid normalizes is_template, pinned, status=pinned, and future defer_until to 0.2');
  assert.deepEqual(hybridStatusParityAudit.batchImpacts, [0.25, 0.25, 0.25, 0.25], 'scoreBatchHybrid honors maxBlockerCount=4');
  assert.equal(hybridStatusParityAudit.hasTemplateCol, true, 'issue_overview_mv exports is_template column');
  assert.equal(hybridStatusParityAudit.hasPinnedCol, true, 'issue_overview_mv exports pinned column');
  await search(page, 'cap-closed-prereq');
  await waitFor(page, `${app}.issues[0]?.id === 'cap-closed-prereq' && ${app}.issues[0]?.text_score === 1`, 'exact closed issue ID recalled #1 with text_score=1.0 in !supportsFTS5 substring mode');
  await search(page, '');
  await resultIDs(page, allCapIDs);

  // Track C (bv-apal.78 / bv-apal.79): Live SQLite + WASM + ForceGraph verification of
  // getTopKSet sequential chaining, parallelCutFiltered, parent-child open-ancestor gates,
  // activeGraph bridges/cut-vertices/k-core/betweenness & #/insights closed/tombstone exclusion,
  // ephemeral 0.2 hybrid parity, and getReferenceNowMs() anchoring of isIssueOverdue, formatDate, and computeBurndownData.
  const trackCAudit = await evaluate(page, `(async () => {
    const SQL = await initSqlJs();
    const savedDbBytes = DB_STATE.db.export();
    try {
      const testDb = new SQL.Database(savedDbBytes);
      ensureSqlFunctions(testDb);
      DB_STATE.db.close();
      DB_STATE.db = testDb;
      DB_STATE.referenceDb = null;
      DB_STATE.referenceNowMs = null;

      // Clear fixture tables in the isolated clone and seed a unified adversarial topology.
      for (const tbl of ['dependencies', 'comments', 'issue_metrics', 'triage_recommendations', 'issue_overview_mv', 'issues']) {
        try { DB_STATE.db.run('DELETE FROM ' + tbl); } catch {}
      }
      DB_STATE.db.run("DELETE FROM export_meta WHERE key = 'resolved_issue_ids'");
      DB_STATE.db.run("INSERT OR REPLACE INTO export_meta (key, value) VALUES ('generated_at', '2026-09-10T12:00:00Z'), ('exported_at', '2026-09-10T12:00:00Z')");

      const refTs = '2026-09-08T12:00:00Z';
      const seedIssues = [
        // 1. Sequential bottleneck A -> B -> {C, D, E} (+ withheld ephemeral & parked review children on B)
        { id: 'seq-a', title: 'Sequential Gate A', status: 'open', priority: 0, type: 'task', ephemeral: 0, actionable: 1, triage: 0.85, blocksCount: 1 },
        { id: 'seq-b', title: 'Sequential Bottleneck B', status: 'open', priority: 1, type: 'task', ephemeral: 0, actionable: 0, triage: 0.70, blocksCount: 5, bw: 5.0 },
        { id: 'seq-c', title: 'Fanout C', status: 'open', priority: 2, type: 'task', ephemeral: 0, actionable: 0, triage: 0.40, blocksCount: 0 },
        { id: 'seq-d', title: 'Fanout D', status: 'open', priority: 2, type: 'task', ephemeral: 0, actionable: 0, triage: 0.40, blocksCount: 0 },
        { id: 'seq-e', title: 'Fanout E', status: 'open', priority: 2, type: 'task', ephemeral: 0, actionable: 0, triage: 0.40, blocksCount: 0 },
        { id: 'seq-ephemeral', title: 'Withheld Ephemeral wispprobe', status: 'open', priority: 1, type: 'task', ephemeral: 1, actionable: 0, triage: 0.30, blocksCount: 0 },
        { id: 'seq-parked', title: 'Withheld Review Task', status: 'review', priority: 1, type: 'task', ephemeral: 0, actionable: 0, triage: 0.30, blocksCount: 0 },
        // Actionable ephemeral blocker with huge fan-out (planted negative control for candidate filtering in topKSet & parallelCutFiltered)
        { id: 'eph-root', title: 'Ephemeral Root wispprobe', status: 'open', priority: 1, type: 'task', ephemeral: 1, actionable: 0, triage: 0.50, blocksCount: 4 },
        { id: 'eph-f1', title: 'Eph Fan 1', status: 'open', priority: 2, type: 'task', ephemeral: 0, actionable: 0, triage: 0.20, blocksCount: 0 },
        { id: 'eph-f2', title: 'Eph Fan 2', status: 'open', priority: 2, type: 'task', ephemeral: 0, actionable: 0, triage: 0.20, blocksCount: 0 },
        { id: 'eph-f3', title: 'Eph Fan 3', status: 'open', priority: 2, type: 'task', ephemeral: 0, actionable: 0, triage: 0.20, blocksCount: 0 },
        { id: 'eph-f4', title: 'Eph Fan 4', status: 'open', priority: 2, type: 'task', ephemeral: 0, actionable: 0, triage: 0.20, blocksCount: 0 },
        // Non-ephemeral open wispprobe + closed ephemeral wispprobe for searchIssues hybrid parity
        { id: 'open-wisp-ctrl', title: 'Standard Open wispprobe', status: 'open', priority: 1, type: 'task', ephemeral: 0, actionable: 1, triage: 0.60, blocksCount: 0 },
        { id: 'closed-wisp-ctrl', title: 'Closed Ephemeral wispprobe', status: 'closed', priority: 1, type: 'task', ephemeral: 1, actionable: 0, triage: 0.10, blocksCount: 0 },
        // 2. parent-child open-ancestor blocking gates vs unblocked open parent vs closed parent
        { id: 'pc-gate', title: 'Parent Gate Blocker', status: 'open', priority: 0, type: 'task', ephemeral: 0, actionable: 1, triage: 0.80, blocksCount: 1 },
        { id: 'pc-blocked-epic', title: 'Blocked Open Epic', status: 'open', priority: 1, type: 'epic', ephemeral: 0, actionable: 0, triage: 0.50, blocksCount: 0 },
        { id: 'pc-blocked-child', title: 'Child Inheriting Open Epic Gate', status: 'open', priority: 1, type: 'task', ephemeral: 0, actionable: 0, triage: 0.50, blocksCount: 0 },
        { id: 'pc-child-own-blocker', title: 'Direct Blocker On Dual-Gated Child', status: 'open', priority: 1, type: 'task', ephemeral: 0, actionable: 1, triage: 0.48, blocksCount: 1 },
        { id: 'pc-dual-gated-child', title: 'Child With Both Blocked Epic And Direct Blocker', status: 'open', priority: 1, type: 'task', ephemeral: 0, actionable: 0, triage: 0.44, blocksCount: 0 },
        { id: 'pc-unblocked-epic', title: 'Unblocked Open Epic', status: 'open', priority: 1, type: 'epic', ephemeral: 0, actionable: 1, triage: 0.55, blocksCount: 0 },
        { id: 'pc-child-prereq', title: 'Direct Prereq Under Unblocked Epic', status: 'open', priority: 1, type: 'task', ephemeral: 0, actionable: 1, triage: 0.65, blocksCount: 1 },
        { id: 'pc-gated-child', title: 'Child Under Unblocked Open Epic', status: 'open', priority: 1, type: 'task', ephemeral: 0, actionable: 0, triage: 0.45, blocksCount: 0 },
        { id: 'pc-stale-blocker', title: 'Stale Open Blocker On Closed Epic', status: 'open', priority: 2, type: 'task', ephemeral: 0, actionable: 1, triage: 0.25, blocksCount: 0 },
        { id: 'pc-closed-epic', title: 'Closed Epic Stops Inheritance', status: 'closed', priority: 1, type: 'epic', ephemeral: 0, actionable: 0, triage: 0.10, blocksCount: 0 },
        { id: 'pc-closed-epic-prereq', title: 'Direct Prereq Under Closed Epic', status: 'open', priority: 1, type: 'task', ephemeral: 0, actionable: 1, triage: 0.60, blocksCount: 1 },
        { id: 'pc-child-under-closed', title: 'Child Under Closed Epic', status: 'open', priority: 1, type: 'task', ephemeral: 0, actionable: 0, triage: 0.45, blocksCount: 0 },
        // 3. Active 3-clique (k-core = 2) + Closed/Tombstone hubs (planted high SQL metrics as negative controls!)
        { id: 'core-1', title: 'Active Clique 1', status: 'open', priority: 1, type: 'task', ephemeral: 0, actionable: 1, triage: 0.62, blocksCount: 2 },
        { id: 'core-2', title: 'Active Clique 2', status: 'open', priority: 1, type: 'task', ephemeral: 0, actionable: 0, triage: 0.58, blocksCount: 1 },
        { id: 'core-3', title: 'Active Clique 3', status: 'open', priority: 1, type: 'task', ephemeral: 0, actionable: 0, triage: 0.54, blocksCount: 0 },
        { id: 'closed-hub', title: 'Planted Closed Hub', status: 'closed', priority: 0, type: 'task', ephemeral: 0, actionable: 0, triage: 0.99, blocksCount: 99, bw: 99.0 },
        { id: 'closed-wing', title: 'Planted Closed Wing', status: 'closed', priority: 2, type: 'task', ephemeral: 0, actionable: 0, triage: 0.95, blocksCount: 50, bw: 50.0 },
        { id: 'tomb-hub', title: 'Planted Tombstone Hub', status: 'tombstone', priority: 0, type: 'task', ephemeral: 0, actionable: 0, triage: 0.98, blocksCount: 88, bw: 88.0 },
      ];

      for (const it of seedIssues) {
        DB_STATE.db.run(
          'INSERT INTO issues (id, title, description, status, priority, issue_type, labels, ephemeral, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)',
          [it.id, it.title, it.title, it.status, it.priority, it.type, '[]', it.ephemeral, '2026-09-01T00:00:00Z', refTs]
        );
        DB_STATE.db.run(
          'INSERT INTO issue_metrics (issue_id, pagerank, betweenness, eigenvector, critical_path_depth, triage_score, blocks_count, blocked_by_count) VALUES (?, ?, ?, ?, ?, ?, ?, ?)',
          [it.id, 0.05, it.bw || 0, 0.05, 1, it.triage, it.blocksCount, it.actionable ? 0 : 1]
        );
        if (it.status !== 'tombstone') {
          DB_STATE.db.run(
            'INSERT INTO issue_overview_mv (id, title, description, status, priority, issue_type, labels, created_at, updated_at, ephemeral, dependency_state, is_actionable, pagerank, betweenness, eigenvector, critical_path_depth, triage_score, blocker_count, dependent_count, comment_count) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)',
            [it.id, it.title, it.title, it.status, it.priority, it.type, '[]', '2026-09-01T00:00:00Z', refTs, it.ephemeral, it.actionable ? 'satisfied' : 'unsatisfied', it.actionable, 0.05, it.bw || 0, 0.05, 1, it.triage, it.actionable ? 0 : 1, it.blocksCount, 0]
          );
        }
      }

      const seedDeps = [
        // Sequential A -> B -> {C, D, E, seq-ephemeral, seq-parked}
        ['seq-b', 'seq-a', 'blocks'],
        ['seq-c', 'seq-b', 'blocks'],
        ['seq-d', 'seq-b', 'blocks'],
        ['seq-e', 'seq-b', 'blocks'],
        ['seq-ephemeral', 'seq-b', 'blocks'],
        ['seq-parked', 'seq-b', 'blocks'],
        // Ephemeral root fan-out
        ['eph-f1', 'eph-root', 'blocks'],
        ['eph-f2', 'eph-root', 'blocks'],
        ['eph-f3', 'eph-root', 'blocks'],
        ['eph-f4', 'eph-root', 'blocks'],
        // parent-child cases
        ['pc-blocked-epic', 'pc-gate', 'blocks'],
        ['pc-blocked-child', 'pc-blocked-epic', 'parent-child'],
        ['pc-dual-gated-child', 'pc-blocked-epic', 'parent-child'],
        ['pc-dual-gated-child', 'pc-child-own-blocker', 'blocks'],
        ['pc-gated-child', 'pc-unblocked-epic', 'parent-child'],
        ['pc-gated-child', 'pc-child-prereq', 'blocks'],
        ['pc-closed-epic', 'pc-stale-blocker', 'blocks'],
        ['pc-child-under-closed', 'pc-closed-epic', 'parent-child'],
        ['pc-child-under-closed', 'pc-closed-epic-prereq', 'blocks'],
        // Active 3-clique
        ['core-2', 'core-1', 'blocks'],
        ['core-3', 'core-1', 'blocks'],
        ['core-3', 'core-2', 'blocks'],
        // Closed/tombstone bridges & clique connected to active graph
        ['closed-hub', 'seq-a', 'blocks'],
        ['closed-wing', 'closed-hub', 'blocks'],
        ['closed-wing', 'seq-a', 'blocks'],
        ['tomb-hub', 'seq-a', 'blocks'],
      ];
      for (const [from, to, kind] of seedDeps) {
        DB_STATE.db.run('INSERT INTO dependencies (issue_id, depends_on_id, type) VALUES (?, ?, ?)', [from, to, kind]);
      }

      // Rebuild WASM graphs on the seeded DB and compute filtered subgraph metrics via recalculateMetrics
      try { DB_STATE.db.run("INSERT INTO issues_fts(issues_fts) VALUES('rebuild')"); } catch {}
      GRAPH_STATE.ready = false;
      await initGraphEngine();
      const baseGraphCounts = [GRAPH_STATE.graph.nodeCount(), GRAPH_STATE.graph.edgeCount()];
      const recalcIds = seedIssues
        .map(i => i.id)
        .sort((a, b) => GRAPH_STATE.nodeMap.get(a) - GRAPH_STATE.nodeMap.get(b));
      const recalc = recalculateMetrics(recalcIds);
      const recalcClosedHubBw = recalc.betweenness[recalcIds.indexOf('closed-hub')];
      const recalcSeqBBw = recalc.betweenness[recalcIds.indexOf('seq-b')];

      // 1. getTopKSet(3) sequential chaining + getParallelCutData(5) parallelCutFiltered
      const topK3 = getTopKSet(3);
      const parallelCut = getParallelCutData(5);
      const rawUnfilteredParallel = GRAPH_STATE.graph.parallelCutSuggestions(
        buildClosedSet().subarray(0, GRAPH_STATE.graph.nodeCount()),
        5
      );
      const rawUnfilteredLeader = rawUnfilteredParallel?.items?.[0]
        ? GRAPH_STATE.graph.nodeId(rawUnfilteredParallel.items[0].node)
        : null;

      // 2. parent-child whatIfClose / whatIfCloseBatch / allWhatIf (viewer.js) & performWhatIf / performBatchWhatIf (graph.js)
      const viewerCloseGate = whatIfClose('pc-gate');
      const viewerCloseBlockedEpic = whatIfClose('pc-blocked-epic');
      const viewerBatchBlockedEpic = window.beadsViewer.whatIfCloseBatch(['pc-blocked-epic']);
      const viewerBatchEpicAndOwnBlocker = window.beadsViewer.whatIfCloseBatch(['pc-blocked-epic', 'pc-child-own-blocker']);
      const allWhatIfRows = window.beadsViewer.allWhatIf(50);
      const allWhatIfBlockedEpic = allWhatIfRows.find(r => r.issueId === 'pc-blocked-epic') || null;
      const viewerCloseChildPrereq = whatIfClose('pc-child-prereq');
      const viewerCloseUnderClosed = whatIfClose('pc-closed-epic-prereq');

      const gvd = getGraphViewData();
      const fgm = ${app}.forceGraphModule;
      fgm.loadData(gvd.issues, gvd.dependencies, null);
      const graphCloseGate = fgm.performWhatIf('pc-gate');
      const graphGateUnblocked = (graphCloseGate?.unblocked_ids || []).map(idx => fgm.getWasmGraph().nodeId(idx));
      const graphCloseBlockedEpic = fgm.performWhatIf('pc-blocked-epic');
      const graphBlockedEpicUnblocked = (graphCloseBlockedEpic?.unblocked_ids || []).map(idx => fgm.getWasmGraph().nodeId(idx));
      const graphBatchSingleEpic = fgm.performBatchWhatIf(['pc-blocked-epic']);
      const graphBatchSingleEpicUnblocked = (graphBatchSingleEpic?.unblocked_ids || []).map(idx => fgm.getWasmGraph().nodeId(idx));
      const graphBatchEpicAndOwn = fgm.performBatchWhatIf(['pc-blocked-epic', 'pc-child-own-blocker']);
      const graphBatchEpicAndOwnUnblocked = (graphBatchEpicAndOwn?.unblocked_ids || []).map(idx => fgm.getWasmGraph().nodeId(idx));
      const graphCloseChildPrereq = fgm.performWhatIf('pc-child-prereq');
      const graphChildPrereqUnblocked = (graphCloseChildPrereq?.unblocked_ids || []).map(idx => fgm.getWasmGraph().nodeId(idx));
      fgm.resetWhatIf();

      // 3. Active-subgraph metrics & #/insights top-N closed/tombstone exclusion
      const bridges = getBridgeDependencies(50);
      const cutVertices = window.beadsViewer.getArticulationPoints(50);
      const topKCore = window.beadsViewer.getTopByKCore(50);
      const topBetweennessSql = getTopByBetweenness(50);
      // Force WASM fallback path on getTopByBetweenness by zeroing SQL betweenness
      DB_STATE.db.run('UPDATE issue_metrics SET betweenness = 0; UPDATE issue_overview_mv SET betweenness = 0');
      const topBetweennessWasm = getTopByBetweenness(50);
      const topTriage = window.beadsViewer.getTopByTriageScore(50);
      const topBlockers = window.beadsViewer.getTopBlockers(50);
      const prScores = GRAPH_STATE.graph.pagerankDefault();
      const rankedPr = getRankedGraphIssues(prScores, 'pagerank', 50);
      const rankedPrAllFiniteNonNeg = rankedPr.length > 8 && rankedPr.every(r => typeof r.pagerank === 'number' && Number.isFinite(r.pagerank) && r.pagerank >= 0);

      const fgMetrics = fgm.getMetrics();
      const closedHubIdx = fgm.getWasmGraph().nodeIdx('closed-hub');
      const tombHubIdx = fgm.getWasmGraph().nodeIdx('tomb-hub');
      const seqBIdx = fgm.getWasmGraph().nodeIdx('seq-b');
      const core1Idx = fgm.getWasmGraph().nodeIdx('core-1');
      const fgNodesById = Object.fromEntries(fgm.getGraph().graphData().nodes.map(n => [n.id, n]));

      // 4. Hybrid ephemeral 0.2 parity in searchIssues + reference clock anchoring
      const wispHybrid = searchIssues('wispprobe', { mode: 'hybrid', preset: 'default', limit: 20 });
      const wispStatusScores = Object.fromEntries(wispHybrid.map(r => [r.id, r.component_scores?.status]));

      const refNowMs = window.beadsViewer.getReferenceNowMs();
      const overdueFutureRelativeToExport = isIssueOverdue({ status: 'open', due_date: '2026-10-01T00:00:00Z' });
      const overduePastRelativeToExport = isIssueOverdue({ status: 'open', due_date: '2026-09-05T00:00:00Z' });
      const formattedTwoDaysAgo = window.beadsViewer.formatDate('2026-09-08T12:00:00Z');
      const burndown = window.bvCharts.computeBurndownData([
        { id: 'bd-1', status: 'open', created_at: '2026-09-01T00:00:00Z' },
      ]);

      return {
        baseGraphCounts,
        recalcClosedHubBw,
        recalcSeqBBw,
        whatIfNodeCount: GRAPH_STATE.whatIfGraph.nodeCount(),
        topKItems: topK3.items.map(i => [i.issueId, i.marginal_gain, i.unblocked_issue_ids]),
        topKTotalGain: topK3.total_gain,
        parallelCutIds: parallelCut.items.map(i => [i.issueId, i.parallel_gain]),
        parallelCutOpenNodes: parallelCut.open_nodes,
        parallelCutCurrentActionable: parallelCut.current_actionable,
        rawUnfilteredLeader,
        viewerCloseGateUnblocked: viewerCloseGate.unblocked_issue_ids.slice().sort(),
        viewerCloseBlockedEpicUnblocked: viewerCloseBlockedEpic.unblocked_issue_ids.slice().sort(),
        viewerBatchBlockedEpicUnblocked: viewerBatchBlockedEpic.unblocked_issue_ids.slice().sort(),
        viewerBatchEpicAndOwnUnblocked: viewerBatchEpicAndOwnBlocker.unblocked_issue_ids.slice().sort(),
        allWhatIfBlockedEpicUnblocked: (allWhatIfBlockedEpic?.result?.unblocked_issue_ids || []).slice().sort(),
        allWhatIfBlockedEpicDirect: allWhatIfBlockedEpic?.result?.direct_unblocks || 0,
        viewerCloseChildPrereqUnblocked: viewerCloseChildPrereq.unblocked_issue_ids.slice().sort(),
        viewerCloseUnderClosedUnblocked: viewerCloseUnderClosed.unblocked_issue_ids.slice().sort(),
        graphGateUnblocked: graphGateUnblocked.slice().sort(),
        graphBlockedEpicUnblocked: graphBlockedEpicUnblocked.slice().sort(),
        graphBatchSingleEpicUnblocked: graphBatchSingleEpicUnblocked.slice().sort(),
        graphBatchEpicAndOwnUnblocked: graphBatchEpicAndOwnUnblocked.slice().sort(),
        graphChildPrereqUnblocked: graphChildPrereqUnblocked.slice().sort(),
        gvdHasEphemeral: gvd.issues.some(i => i.id === 'seq-ephemeral' && Number(i.ephemeral) === 1),
        bridgesFromTo: bridges.map(b => b.from + '->' + b.to),
        cutVertexIds: cutVertices.map(v => v.id),
        topKCoreScores: Object.fromEntries(topKCore.map(r => [r.id, r.kcore])),
        topBetweennessSqlIds: topBetweennessSql.map(r => r.id),
        topBetweennessSqlSeqB: topBetweennessSql.find(r => r.id === 'seq-b')?.betweenness || 0,
        topBetweennessWasmIds: topBetweennessWasm.map(r => r.id),
        topBetweennessWasmSeqB: topBetweennessWasm.find(r => r.id === 'seq-b')?.betweenness || 0,
        topTriageIds: topTriage.map(r => r.id),
        topBlockerIds: topBlockers.map(r => r.id),
        rankedPrIds: rankedPr.map(r => r.id),
        rankedPrAllFiniteNonNeg,
        fgVectorLen: fgMetrics.betweenness.length,
        fgWasmLen: fgm.getWasmGraph().nodeCount(),
        fgClosedBw: fgMetrics.betweenness[closedHubIdx],
        fgTombBw: fgMetrics.betweenness[tombHubIdx],
        fgSeqBBw: fgMetrics.betweenness[seqBIdx],
        fgClosedKCore: fgMetrics.kcore[closedHubIdx],
        fgCore1KCore: fgMetrics.kcore[core1Idx],
        fgClosedArticulation: fgNodesById['closed-hub']?.isArticulationPoint,
        fgSeqBArticulation: fgNodesById['seq-b']?.isArticulationPoint,
        wispStatusScores,
        refNowIso: new Date(refNowMs).toISOString(),
        overdueFutureRelativeToExport,
        overduePastRelativeToExport,
        formattedTwoDaysAgo,
        burndownLastLabel: burndown.labels.at(-1),
        burndownLabelCount: burndown.labels.length,
      };
    } finally {
      // Restore exact original capabilities SQLite DB, WASM engines, and force-graph state.
      try { DB_STATE.db.close(); } catch {}
      DB_STATE.db = new SQL.Database(savedDbBytes);
      ensureSqlFunctions(DB_STATE.db);
      DB_STATE.referenceDb = null;
      DB_STATE.referenceNowMs = null;
      GRAPH_STATE.ready = false;
      await initGraphEngine();
      const origGvd = getGraphViewData();
      ${app}.forceGraphModule.loadData(origGvd.issues, origGvd.dependencies, null);
    }
  })()`);

  // Assert Task 1 & [C-1]/[C-5]: getTopKSet sequential chaining, parallelCutFiltered + lifecycle open_nodes, and parent-child gates
  assert.deepEqual(trackCAudit.baseGraphCounts, [32, 22], 'GRAPH_STATE.graph retains exact 32 nodes and 22 IsBlocking edges (excluding 4 parent-child edges and sentinel)');
  assert.equal(trackCAudit.whatIfNodeCount, 33, 'companion whatIfGraph appends single unresolved sentinel at index N=32');
  assert.deepEqual(
    trackCAudit.topKItems,
    [
      ['pc-gate', 2, ['pc-blocked-child', 'pc-blocked-epic']],
      ['seq-a', 1, ['seq-b']],
      ['seq-b', 3, ['seq-c', 'seq-d', 'seq-e']],
    ],
    `getTopKSet(3) honors parent-child gates and chains sequential bottleneck seq-a -> seq-b -> {seq-c,seq-d,seq-e} while withholding ephemeral & parked review rows: ${JSON.stringify(trackCAudit.topKItems)}`
  );
  assert.equal(trackCAudit.topKTotalGain, 6, 'getTopKSet(3) total_gain sums step-1 (2) + step-2 (1) + chained step-3 (3) unlocks');
  assert.equal(trackCAudit.rawUnfilteredLeader, 'eph-root', 'planted negative control: unfiltered parallelCutSuggestions would pick non-candidate eph-root');
  assert.deepEqual(
    trackCAudit.parallelCutIds,
    [['pc-gate', 1]],
    `getParallelCutData(parallelCutFiltered) excludes ephemeral eph-root & blocked seq-b and selects actionable pc-gate (parallel_gain=1): ${JSON.stringify(trackCAudit.parallelCutIds)}`
  );
  assert.equal(trackCAudit.parallelCutOpenNodes, 27, '[C-5] getParallelCutData.open_nodes reports true non-closed/non-tombstone node count (32 - 5 = 27)');
  assert.ok(trackCAudit.parallelCutCurrentActionable > 0 && trackCAudit.parallelCutCurrentActionable < trackCAudit.parallelCutOpenNodes, 'getParallelCutData.current_actionable remains candidate-actionable subset');
  assert.deepEqual(trackCAudit.viewerCloseGateUnblocked, ['pc-blocked-child', 'pc-blocked-epic'], 'viewer.js whatIfClose(pc-gate) unlocks both open epic and its parent-child subtask');
  assert.deepEqual(trackCAudit.graphGateUnblocked, ['pc-blocked-child', 'pc-blocked-epic'], 'graph.js performWhatIf(pc-gate) unlocks both open epic and its parent-child subtask');
  // [C-1] Simulating closing a blocked open parent epic stops ancestor inheritance while keeping child's own direct blockers enforced!
  assert.deepEqual(trackCAudit.viewerCloseBlockedEpicUnblocked, ['pc-blocked-child'], '[C-1] viewer.js whatIfClose(pc-blocked-epic) unblocks pc-blocked-child while withholding pc-dual-gated-child (own blocker open)');
  assert.deepEqual(trackCAudit.viewerBatchBlockedEpicUnblocked, ['pc-blocked-child'], '[C-1] viewer.js whatIfCloseBatch([pc-blocked-epic]) unblocks pc-blocked-child and withholds pc-dual-gated-child');
  assert.deepEqual(trackCAudit.viewerBatchEpicAndOwnUnblocked, ['pc-blocked-child', 'pc-dual-gated-child'], '[C-1] viewer.js whatIfCloseBatch([pc-blocked-epic, pc-child-own-blocker]) unblocks both children');
  assert.equal(trackCAudit.allWhatIfBlockedEpicDirect, 1, '[C-1] viewer.js allWhatIf() evaluates blocked open parent epic with direct_unblocks=1');
  assert.deepEqual(trackCAudit.allWhatIfBlockedEpicUnblocked, ['pc-blocked-child'], '[C-1] viewer.js allWhatIf() unblocks pc-blocked-child for pc-blocked-epic');
  assert.deepEqual(trackCAudit.graphBlockedEpicUnblocked, ['pc-blocked-child'], '[C-1] graph.js performWhatIf(pc-blocked-epic) unblocks pc-blocked-child while pc-gate is open');
  assert.deepEqual(trackCAudit.graphBatchSingleEpicUnblocked, ['pc-blocked-child'], '[C-1] graph.js performBatchWhatIf([pc-blocked-epic]) unblocks pc-blocked-child');
  assert.deepEqual(trackCAudit.graphBatchEpicAndOwnUnblocked, ['pc-blocked-child', 'pc-dual-gated-child'], '[C-1] graph.js performBatchWhatIf([pc-blocked-epic, pc-child-own-blocker]) unblocks both children');
  assert.deepEqual(trackCAudit.viewerCloseChildPrereqUnblocked, ['pc-gated-child'], 'unblocked open parent pc-unblocked-epic does NOT block pc-gated-child');
  assert.deepEqual(trackCAudit.graphChildPrereqUnblocked, ['pc-gated-child'], 'graph.js performWhatIf confirms unblocked open parent does NOT block child');
  assert.deepEqual(trackCAudit.viewerCloseUnderClosedUnblocked, ['pc-child-under-closed'], 'closed parent pc-closed-epic stops open-ancestor blocker inheritance');
  assert.equal(trackCAudit.gvdHasEphemeral, true, 'getGraphViewData exposes ephemeral column to graph.js');

  // Assert Task 2 & [C-2]: activeGraph metrics & closed/tombstone exclusion across viewer.js and graph.js
  assert.equal(trackCAudit.rankedPrIds.length, 27, '[C-2] getRankedGraphIssues(prScores, "pagerank", 50) ranks all 27 active visible issues (> 8)');
  assert.equal(trackCAudit.rankedPrAllFiniteNonNeg, true, '[C-2] getRankedGraphIssues populates finite non-negative numeric pagerank on every returned issue');
  for (const resolvedId of ['closed-hub', 'closed-wing', 'tomb-hub']) {
    assert.equal(trackCAudit.bridgesFromTo.some(e => e.includes(resolvedId)), false, `getBridgeDependencies excludes ${resolvedId}`);
    assert.equal(trackCAudit.cutVertexIds.includes(resolvedId), false, `getArticulationPoints excludes ${resolvedId}`);
    assert.equal(Object.hasOwn(trackCAudit.topKCoreScores, resolvedId), false, `getTopByKCore excludes ${resolvedId}`);
    assert.equal(trackCAudit.topBetweennessSqlIds.includes(resolvedId), false, `getTopByBetweenness (SQL) excludes ${resolvedId}`);
    assert.equal(trackCAudit.topBetweennessWasmIds.includes(resolvedId), false, `getTopByBetweenness (WASM fallback) excludes ${resolvedId}`);
    assert.equal(trackCAudit.topTriageIds.includes(resolvedId), false, `getTopByTriageScore excludes ${resolvedId} despite planted 0.99 score`);
    assert.equal(trackCAudit.topBlockerIds.includes(resolvedId), false, `getTopBlockers excludes ${resolvedId} despite planted 99 blocks_count`);
    assert.equal(trackCAudit.rankedPrIds.includes(resolvedId), false, `getRankedGraphIssues excludes ${resolvedId}`);
  }
  assert.equal(trackCAudit.bridgesFromTo.includes('seq-b->seq-a'), true, 'getBridgeDependencies detects active bridge seq-b->seq-a');
  assert.equal(trackCAudit.cutVertexIds.includes('seq-b'), true, 'getArticulationPoints detects active cut vertex seq-b');
  assert.equal(trackCAudit.topKCoreScores['core-1'], 2, 'getTopByKCore computes active 3-clique k-core = 2');
  assert.ok(trackCAudit.topBetweennessSqlSeqB > 0 && trackCAudit.topBetweennessWasmSeqB > 0, 'seq-b has positive active betweenness in both SQL and WASM fallback');
  assert.equal(trackCAudit.recalcClosedHubBw, 0, 'recalculateMetrics zeros closed-hub betweenness via activeGraph');
  assert.ok(trackCAudit.recalcSeqBBw > 0, 'recalculateMetrics computes positive active betweenness for seq-b');
  assert.equal(trackCAudit.fgVectorLen, trackCAudit.fgWasmLen, 'graph.js projects active betweenness to full wasmGraph length');
  assert.equal(trackCAudit.fgClosedBw, 0, 'graph.js projects 0 betweenness for closed-hub');
  assert.equal(trackCAudit.fgTombBw, 0, 'graph.js projects 0 betweenness for tomb-hub');
  assert.ok(trackCAudit.fgSeqBBw > 0, 'graph.js projects positive active betweenness for seq-b');
  assert.equal(trackCAudit.fgClosedKCore, 0, 'graph.js projects 0 k-core for closed-hub');
  assert.equal(trackCAudit.fgCore1KCore, 2, 'graph.js projects active k-core=2 for core-1');
  assert.equal(trackCAudit.fgClosedArticulation, false, 'graph.js suppresses isArticulationPoint on closed-hub');
  assert.equal(trackCAudit.fgSeqBArticulation, true, 'graph.js sets isArticulationPoint=true on active cut vertex seq-b');

  // Assert Task 3: ephemeral 0.2 hybrid parity & getReferenceNowMs() wall-clock anchoring
  assert.deepEqual(trackCAudit.wispStatusScores, {
    'open-wisp-ctrl': 1.0,
    'eph-root': 0.2,
    'seq-ephemeral': 0.2,
    'closed-wisp-ctrl': 0.1,
  }, 'searchIssues hybrid scoring downgrades open ephemeral rows to 0.2 while keeping open=1.0 and closed=0.1');
  assert.equal(trackCAudit.refNowIso, '2026-09-10T12:00:00.000Z', 'getReferenceNowMs anchors to SQLite export_meta timestamp');
  assert.equal(trackCAudit.overdueFutureRelativeToExport, false, 'isIssueOverdue anchors to getReferenceNowMs (2026-10-01 is NOT overdue at 2026-09-10 export time)');
  assert.equal(trackCAudit.overduePastRelativeToExport, true, 'isIssueOverdue flags due_date before export reference time');
  assert.equal(trackCAudit.formattedTwoDaysAgo, '2d ago', 'formatDate anchors relative age to getReferenceNowMs (2026-09-08 vs 2026-09-10 = 2d ago)');
  assert.ok(trackCAudit.burndownLastLabel?.startsWith('Sep '), `computeBurndownData anchors maxDate to Sep 2026 reference clock instead of Nov 2026 wall-clock Date.now(): ${trackCAudit.burndownLastLabel}`);
  assert.equal(trackCAudit.burndownLabelCount, 10, 'computeBurndownData emits exact 10 daily buckets from 2026-09-01 through 2026-09-10');

  // Wave 18 (bv-apal.86): Active-PR hybrid search normalization, WASM fallback metrics/cycles,
  // SQLite DAG eigenvector preservation, kcore self-loop == 0, deterministic bridges/betweennessApprox, and styles.css?v= cache-busting.
  const wave18BrowserAudit = await evaluate(page, `(async () => {
    const stylesheetHref = document.querySelector('link[rel="stylesheet"]')?.getAttribute('href') || '';
    const refTs = new Date(getReferenceNowMs()).toISOString();

    // 1. Active-PR Hybrid Search Normalization (positive + planted negative controls)
    DB_STATE.db.run(
      "INSERT INTO issues (id, title, description, status, priority, issue_type, labels, created_at, updated_at) VALUES " +
      "('w18-closed-pr-hub', 'zirconquartz probe item', 'zirconquartz probe', 'closed', 1, 'task', '[]', '2026-09-01T00:00:00Z', ?), " +
      "('w18-open-pr-high', 'zirconquartz probe item', 'zirconquartz probe', 'open', 1, 'task', '[]', '2026-09-01T00:00:00Z', ?), " +
      "('w18-open-pr-low', 'zirconquartz probe item', 'zirconquartz probe', 'open', 1, 'task', '[]', '2026-09-01T00:00:00Z', ?)",
      [refTs, refTs, refTs]
    );
    DB_STATE.db.run(
      "INSERT INTO issue_metrics (issue_id, pagerank, betweenness, eigenvector, critical_path_depth, triage_score, blocks_count, blocked_by_count) VALUES " +
      "('w18-closed-pr-hub', 9.0, 0, 0, 0, 0.1, 0, 0), " +
      "('w18-open-pr-high', 0.9, 0, 0, 0, 0.8, 0, 0), " +
      "('w18-open-pr-low', 0.09, 0, 0, 0, 0.4, 0, 0)"
    );
    DB_STATE.db.run(
      "INSERT INTO issue_overview_mv (id, title, description, status, priority, issue_type, labels, created_at, updated_at, dependency_state, is_actionable, pagerank, betweenness, eigenvector, critical_path_depth, triage_score, blocks_count, blocked_by_count, blocker_count, dependent_count, critical_depth, in_cycle, comment_count) VALUES " +
      "('w18-closed-pr-hub', 'zirconquartz probe item', 'zirconquartz probe', 'closed', 1, 'task', '[]', '2026-09-01T00:00:00Z', ?, 'satisfied', 0, 9.0, 0, 0, 0, 0.1, 0, 0, 0, 0, 0, 0, 0), " +
      "('w18-open-pr-high', 'zirconquartz probe item', 'zirconquartz probe', 'open', 1, 'task', '[]', '2026-09-01T00:00:00Z', ?, 'satisfied', 1, 0.9, 0, 0, 0, 0.8, 0, 0, 0, 0, 0, 0, 0), " +
      "('w18-open-pr-low', 'zirconquartz probe item', 'zirconquartz probe', 'open', 1, 'task', '[]', '2026-09-01T00:00:00Z', ?, 'satisfied', 1, 0.09, 0, 0, 0, 0.4, 0, 0, 0, 0, 0, 0, 0)",
      [refTs, refTs, refTs]
    );
    if (supportsFTS5()) {
      DB_STATE.db.run("INSERT INTO issues_fts(issues_fts) VALUES('rebuild')");
    }

    let prBounds = null;
    let hybridRows = [];
    let fallbackCycleWithCycles = null;
    let fallbackCyclePhase2Timeout = null;
    let fallbackCycleAcyclic = null;
    let wasmFallbackGraphAudit = null;
    let dagEigenvectorAudit = null;
    let wasmGraphAlgAudit = null;

    const savedGraphReady = GRAPH_STATE.ready;
    const savedBvGraphWasm = window.bvGraphWasm;
    try {
      prBounds = getPageRankBounds();
      hybridRows = searchIssues('zirconquartz', { mode: 'hybrid', preset: 'impact-first', limit: 10 });

      // 2. WASM Fallback Mode: getCycleInfo() when !GRAPH_STATE.ready
      GRAPH_STATE.ready = false;
      fallbackCycleWithCycles = getCycleInfo({ project_health: { graph: { has_cycles: true, cycle_count: 2 } } });
      DB_STATE.db.run("UPDATE issue_overview_mv SET in_cycle = 0 WHERE id IN ('cap-auth-root', 'cap-auth-ui', 'cap-billing-a', 'cap-billing-b')");
      try {
        const savedDataGraphHealth = (typeof DATA === 'object' && DATA) ? DATA.graphHealth : undefined;
        if (typeof DATA === 'object' && DATA) {
          DATA.graphHealth = { has_cycles: false, cycle_count: 0 };
        }
        try {
          // Positive Phase-2-timeout / XL / BV_SKIP_PHASE2 recovery: live SQLite dependencies still contain active cycles.
          fallbackCyclePhase2Timeout = getCycleInfo({
            project_health: { graph: { has_cycles: false, cycle_count: 0 } },
            graphHealth: { has_cycles: false, cycle_count: 0 },
          });
          // Genuinely acyclic negative control: convert cyclic back-edges to non-blocking 'related' in SQLite dependencies.
          DB_STATE.db.run("UPDATE dependencies SET type = 'related' WHERE (issue_id = 'cap-auth-root' AND depends_on_id = 'cap-auth-ui') OR (issue_id = 'cap-billing-a' AND depends_on_id = 'cap-billing-b')");
          try {
            fallbackCycleAcyclic = getCycleInfo({
              project_health: { graph: { has_cycles: false, cycle_count: 0 } },
              graphHealth: { has_cycles: false, cycle_count: 0 },
            });
          } finally {
            DB_STATE.db.run("UPDATE dependencies SET type = 'blocks' WHERE (issue_id = 'cap-auth-root' AND depends_on_id = 'cap-auth-ui') OR (issue_id = 'cap-billing-a' AND depends_on_id = 'cap-billing-b')");
          }
        } finally {
          if (typeof DATA === 'object' && DATA) {
            DATA.graphHealth = savedDataGraphHealth;
          }
        }
      } finally {
        DB_STATE.db.run("UPDATE issue_overview_mv SET in_cycle = 1 WHERE id IN ('cap-auth-root', 'cap-auth-ui', 'cap-billing-a', 'cap-billing-b')");
      }
      GRAPH_STATE.ready = savedGraphReady;

      // 3. graph.js WASM Fallback Mode (!store.wasmReady) precomputed metrics, Tarjan SCC disjoint cycles, & Cycle Navigator
      const m = ${app}.forceGraphModule;
      const gd = getGraphViewData();
      window.bvGraphWasm = undefined;
      await m.initGraph('graph-container');
      const fbGraphData = m.loadData(gd.issues, gd.dependencies, null);
      const fbAuthRoot = fbGraphData.nodes.find(n => n.id === 'cap-auth-root');
      const fbClosed = fbGraphData.nodes.find(n => n.id === 'cap-closed-prereq');
      const fbCycleInfo = m.getCycleInfo();
      m.highlightCycles();
      const fbNavInit = m.initCycleNavigator();
      const fbCycle0 = m.highlightCycle(0, false);
      // Verify fake cross-SCC link cap-auth-ui -> cap-billing-a is NEVER highlighted when highlighting cycle 0 or 1:
      const fakeLinkDimmedOnCyc0 = (() => {
        const g = m.getGraph();
        // Temporarily test link opacity through graphData links vs synthetic cross-SCC edge
        const realAuthLink = fbGraphData.links.find(l => (l.source?.id || l.source) === 'cap-auth-root' && (l.target?.id || l.target) === 'cap-auth-ui');
        const billingLink = fbGraphData.links.find(l => (l.source?.id || l.source) === 'cap-billing-a' && (l.target?.id || l.target) === 'cap-billing-b');
        const hubLeafLink = fbGraphData.links.find(l => (l.source?.id || l.source) === 'cap-hub-leaf' && (l.target?.id || l.target) === 'cap-auth-root');
        return {
          hasRealAuthLink: Boolean(realAuthLink),
          hasBillingLink: Boolean(billingLink),
          hasHubLeafLink: Boolean(hubLeafLink),
          cyc0HighlightedNodes: fbGraphData.nodes.filter(n => n._cycleHighlight).map(n => n.id).sort(),
        };
      })();
      const fbCycle1 = m.highlightCycle(1, false);
      const cyc1HighlightedNodes = fbGraphData.nodes.filter(n => n._cycleHighlight).map(n => n.id).sort();
      m.resetCycleNavigator();
      wasmFallbackGraphAudit = {
        wasmReady: m.isWasmReady(),
        hasOverviewEigenvector: gd.issues.some(i => (i.eigenvector || 0) > 0),
        hasOverviewInCycle: gd.issues.some(i => i.in_cycle === true),
        authRootPagerank: fbAuthRoot?.pagerank || 0,
        authRootEigenvector: fbAuthRoot?.eigenvector || 0,
        authRootInCycle: Boolean(fbAuthRoot?.inCycle),
        closedInCycle: Boolean(fbClosed?.inCycle),
        fbCycleInfo,
        fbNavCycleCount: fbNavInit?.cycleCount || 0,
        fbNavCycles: (fbNavInit?.cycles || []).map(c => [...c].sort()),
        cyc0HighlightedNodes: fakeLinkDimmedOnCyc0.cyc0HighlightedNodes,
        cyc1HighlightedNodes,
        cyc0Path: fbCycle0?.path || '',
        cyc1Path: fbCycle1?.path || '',
      };

      // Restore window.bvGraphWasm and verify DAG differentiated eigenvector preservation in graph.js
      window.bvGraphWasm = savedBvGraphWasm;
      await m.initGraph('graph-container');
      const dagIssues = [
        { id: 'dag-a', title: 'DAG Root', status: 'open', priority: 1, type: 'task', eigenvector: 0.85, pagerank: 0.5, betweenness: 0, critical_path_depth: 2, blocker_count: 0, dependent_count: 1, dependency_state: 'satisfied', is_actionable: true },
        { id: 'dag-b', title: 'DAG Mid', status: 'open', priority: 1, type: 'task', eigenvector: 0.45, pagerank: 0.3, betweenness: 1, critical_path_depth: 1, blocker_count: 1, dependent_count: 1, dependency_state: 'unsatisfied', is_actionable: false },
        { id: 'dag-c', title: 'DAG Leaf', status: 'open', priority: 2, type: 'task', eigenvector: 0.10, pagerank: 0.2, betweenness: 0, critical_path_depth: 0, blocker_count: 1, dependent_count: 0, dependency_state: 'unsatisfied', is_actionable: false },
      ];
      const dagDeps = [
        { issue_id: 'dag-b', depends_on_id: 'dag-a', type: 'blocks' },
        { issue_id: 'dag-c', depends_on_id: 'dag-b', type: 'blocks' },
      ];
      const dagLoaded = m.loadData(dagIssues, dagDeps, null);
      const rawWasmDagEig = Array.from(m.getWasmGraph().eigenvectorDefault());
      const preservedDagEig = Array.from(m.getMetrics().eigenvector);
      m.setSizeMetric('eigenvector');
      const sizeDagA = m.getNodeSize(dagLoaded.nodes.find(n => n.id === 'dag-a'));
      const sizeDagC = m.getNodeSize(dagLoaded.nodes.find(n => n.id === 'dag-c'));
      m.setSizeMetric('pagerank');

      // Negative control: cyclic asymmetric graph where WASM eigenvector is non-flat keeps WASM scores
      const cycDeps = [
        { issue_id: 'dag-a', depends_on_id: 'dag-b', type: 'blocks' },
        { issue_id: 'dag-b', depends_on_id: 'dag-a', type: 'blocks' },
        { issue_id: 'dag-c', depends_on_id: 'dag-a', type: 'blocks' },
      ];
      m.loadData(dagIssues, cycDeps, null);
      const rawWasmCycEig = Array.from(m.getWasmGraph().eigenvectorDefault());
      const storedCycEig = Array.from(m.getMetrics().eigenvector);
      dagEigenvectorAudit = {
        rawWasmDagRange: Math.max(...rawWasmDagEig) - Math.min(...rawWasmDagEig),
        preservedDagEig,
        sizeDagA,
        sizeDagC,
        rawWasmCycRange: Math.max(...rawWasmCycEig) - Math.min(...rawWasmCycEig),
        cyclicMatchesWasm: JSON.stringify(rawWasmCycEig) === JSON.stringify(storedCycEig),
      };

      // 4. WASM kcore() self-loop == 0 & deterministic bridges() / betweennessApprox()
      const testGraph = new window.bvGraphWasm.DiGraph();
      try {
        const s0 = testGraph.addNode('solo');
        const l1 = testGraph.addNode('leaf');
        const c2 = testGraph.addNode('c2');
        const c3 = testGraph.addNode('c3');
        const c4 = testGraph.addNode('c4');
        testGraph.addEdge(s0, s0); // self-loop on isolated node
        testGraph.addEdge(l1, l1); // self-loop on pendant
        testGraph.addEdge(l1, c2); // bridge l1 -- c2
        testGraph.addEdge(c2, c2); // self-loop on clique node
        testGraph.addEdge(c2, c3);
        testGraph.addEdge(c3, c4);
        testGraph.addEdge(c4, c2);
        const kcores = Array.from(testGraph.kcore());
        const bridges1 = testGraph.bridges();
        const bridges2 = testGraph.bridges();
        const approx1 = Array.from(testGraph.betweennessApprox(2));
        const approx2 = Array.from(testGraph.betweennessApprox(2));
        const approxZero = Array.from(testGraph.betweennessApprox(0));
        wasmGraphAlgAudit = {
          kcores,
          bridges1,
          bridgesEqual: JSON.stringify(bridges1) === JSON.stringify(bridges2),
          approxEqual: JSON.stringify(approx1) === JSON.stringify(approx2),
          approxZero,
          approxZeroAllFinite: approxZero.every(Number.isFinite),
        };
      } finally {
        testGraph.free();
      }
    } finally {
      GRAPH_STATE.ready = savedGraphReady;
      window.bvGraphWasm = savedBvGraphWasm;
      DB_STATE.db.run("DELETE FROM issues WHERE id IN ('w18-closed-pr-hub', 'w18-open-pr-high', 'w18-open-pr-low')");
      DB_STATE.db.run("DELETE FROM issue_metrics WHERE issue_id IN ('w18-closed-pr-hub', 'w18-open-pr-high', 'w18-open-pr-low')");
      DB_STATE.db.run("DELETE FROM issue_overview_mv WHERE id IN ('w18-closed-pr-hub', 'w18-open-pr-high', 'w18-open-pr-low')");
      if (supportsFTS5()) {
        DB_STATE.db.run("INSERT INTO issues_fts(issues_fts) VALUES('rebuild')");
      }
      ${app}.forceGraphReady = false;
      await ${app}.initForceGraphView();
    }

    return {
      stylesheetHref,
      prBounds,
      hybridSummary: hybridRows.map(r => ({
        id: r.id,
        score: r.hybrid_score,
        prComp: r.component_scores?.pagerank,
        stComp: r.component_scores?.status,
      })),
      fallbackCycleWithCycles,
      fallbackCyclePhase2Timeout,
      fallbackCycleAcyclic,
      wasmFallbackGraphAudit,
      dagEigenvectorAudit,
      wasmGraphAlgAudit,
    };
  })()`);
  assert.match(wave18BrowserAudit.stylesheetHref, /^styles\.css\?v=/, 'index.html cache-busts styles.css?v=...');
  assert.ok(Math.abs(wave18BrowserAudit.prBounds.maxPageRank - 0.9) < 1e-6, `getPageRankBounds prefers active maxPageRank (0.9) over closed mega-hub (9.0): ${JSON.stringify(wave18BrowserAudit.prBounds)}`);
  const w18HybridById = new Map(wave18BrowserAudit.hybridSummary.map(r => [r.id, r]));
  assert.equal(w18HybridById.get('w18-open-pr-high')?.prComp, 1.0, 'top active issue receives normalized PageRank 1.0 instead of 0.1');
  assert.ok(Math.abs((w18HybridById.get('w18-open-pr-low')?.prComp || 0) - 0.1) < 1e-6, 'lower active issue scales proportionally against active max');
  assert.equal(w18HybridById.get('w18-closed-pr-hub')?.prComp, 1.0, 'closed mega-hub with raw PR > active max is clamped to 1.0');
  assert.ok((w18HybridById.get('w18-open-pr-high')?.score || 0) > (w18HybridById.get('w18-open-pr-low')?.score || 0), 'high-PR open issue outranks low-PR open issue');
  assert.ok((w18HybridById.get('w18-open-pr-high')?.score || 0) > (w18HybridById.get('w18-closed-pr-hub')?.score || 0), 'high-PR open issue outranks clamped closed mega-hub');
  assert.equal(wave18BrowserAudit.fallbackCycleWithCycles.hasCycles, true, 'getCycleInfo WASM fallback reports active cycles');
  assert.equal(wave18BrowserAudit.fallbackCycleWithCycles.cycleCount, 2, 'getCycleInfo WASM fallback computes 2 disjoint cycles');
  assert.equal(wave18BrowserAudit.fallbackCycleWithCycles.sccCount, 2, 'getCycleInfo WASM fallback separates disjoint SCCs');
  assert.equal(wave18BrowserAudit.fallbackCycleWithCycles.largestSccSize, 2, 'getCycleInfo WASM fallback reports max SCC size 2 (never lumping 4 nodes)');
  assert.deepEqual(wave18BrowserAudit.fallbackCycleWithCycles.sccComponents, [['cap-auth-root', 'cap-auth-ui'], ['cap-billing-a', 'cap-billing-b']], 'getCycleInfo WASM fallback keeps disjoint cycles as separate SCCs');
  assert.equal(wave18BrowserAudit.fallbackCyclePhase2Timeout.hasCycles, true, 'getCycleInfo WASM fallback recovers live SQLite cycles even when in_cycle=0 and health reports acyclic');
  assert.equal(wave18BrowserAudit.fallbackCyclePhase2Timeout.cycleCount, 2, 'Phase-2-timeout fallback recovers both active cycles');
  assert.equal(wave18BrowserAudit.fallbackCyclePhase2Timeout.count, 2, 'Phase-2-timeout fallback sets count=2');
  assert.equal(wave18BrowserAudit.fallbackCyclePhase2Timeout.sccCount, 2, 'Phase-2-timeout fallback sets sccCount=2');
  assert.deepEqual(wave18BrowserAudit.fallbackCyclePhase2Timeout.sccComponents, [['cap-auth-root', 'cap-auth-ui'], ['cap-billing-a', 'cap-billing-b']], 'Phase-2-timeout fallback separates both disjoint SCCs');
  assert.equal(wave18BrowserAudit.fallbackCycleAcyclic.hasCycles, false, 'getCycleInfo WASM fallback negative control returns false when SQLite graph is genuinely acyclic');
  assert.equal(wave18BrowserAudit.fallbackCycleAcyclic.cycleCount, 0);
  assert.equal(wave18BrowserAudit.fallbackCycleAcyclic.count, 0);
  assert.equal(wave18BrowserAudit.fallbackCycleAcyclic.sccCount, 0);
  assert.deepEqual(wave18BrowserAudit.fallbackCycleAcyclic.sccComponents, []);
  assert.equal(wave18BrowserAudit.wasmFallbackGraphAudit.wasmReady, false);
  assert.equal(wave18BrowserAudit.wasmFallbackGraphAudit.hasOverviewEigenvector, true);
  assert.equal(wave18BrowserAudit.wasmFallbackGraphAudit.hasOverviewInCycle, true);
  assert.ok(wave18BrowserAudit.wasmFallbackGraphAudit.authRootPagerank > 0, 'graph.js WASM fallback populates node.pagerank');
  assert.ok(wave18BrowserAudit.wasmFallbackGraphAudit.authRootEigenvector > 0, 'graph.js WASM fallback populates node.eigenvector');
  assert.equal(wave18BrowserAudit.wasmFallbackGraphAudit.authRootInCycle, true, 'graph.js WASM fallback marks active cycle node inCycle=true');
  assert.equal(wave18BrowserAudit.wasmFallbackGraphAudit.closedInCycle, false, 'graph.js WASM fallback keeps closed node inCycle=false');
  assert.equal(wave18BrowserAudit.wasmFallbackGraphAudit.fbCycleInfo.cycleCount, 2, 'graph.js getCycleInfo() reports 2 cycles when wasmReady=false');
  assert.equal(wave18BrowserAudit.wasmFallbackGraphAudit.fbNavCycleCount, 2, 'graph.js initCycleNavigator() finds 2 separate cycles when wasmReady=false');
  assert.deepEqual(wave18BrowserAudit.wasmFallbackGraphAudit.fbNavCycles, [['cap-auth-root', 'cap-auth-ui'], ['cap-billing-a', 'cap-billing-b']], 'graph.js initCycleNavigator() keeps disjoint cycles separate without merging B->C');
  assert.deepEqual(wave18BrowserAudit.wasmFallbackGraphAudit.cyc0HighlightedNodes, ['cap-auth-root', 'cap-auth-ui'], 'highlightCycle(0) highlights only cycle 1');
  assert.deepEqual(wave18BrowserAudit.wasmFallbackGraphAudit.cyc1HighlightedNodes, ['cap-billing-a', 'cap-billing-b'], 'highlightCycle(1) highlights only cycle 2');
  assert.ok(!wave18BrowserAudit.wasmFallbackGraphAudit.cyc0Path.includes('cap-billing-a') && !wave18BrowserAudit.wasmFallbackGraphAudit.cyc1Path.includes('cap-auth-ui'), 'fallback cycle paths never fabricate cross-SCC edge B->C');
  assert.ok(wave18BrowserAudit.dagEigenvectorAudit.rawWasmDagRange < 1e-9, 'WASM power-iteration eigenvector on DAG is flat');
  assert.ok(Math.abs(wave18BrowserAudit.dagEigenvectorAudit.preservedDagEig[0] - 0.85) < 1e-6 && Math.abs(wave18BrowserAudit.dagEigenvectorAudit.preservedDagEig[2] - 0.10) < 1e-6, 'graph.js preserves differentiated SQLite eigenvector on DAGs');
  assert.ok(wave18BrowserAudit.dagEigenvectorAudit.sizeDagA > wave18BrowserAudit.dagEigenvectorAudit.sizeDagC, 'sizeMetric="eigenvector" differentiates DAG nodes');
  assert.ok(wave18BrowserAudit.dagEigenvectorAudit.rawWasmCycRange > 1e-4 && wave18BrowserAudit.dagEigenvectorAudit.cyclicMatchesWasm, 'non-flat cyclic WASM eigenvector remains authoritative');
  assert.deepEqual(wave18BrowserAudit.wasmGraphAlgAudit.kcores, [0, 1, 2, 2, 2], 'WASM kcore skips self-loops while preserving 2-core triangle');
  assert.deepEqual(wave18BrowserAudit.wasmGraphAlgAudit.bridges1, [[1, 2]], 'WASM bridges returns canonical sorted bridge');
  assert.equal(wave18BrowserAudit.wasmGraphAlgAudit.bridgesEqual, true);
  assert.equal(wave18BrowserAudit.wasmGraphAlgAudit.approxEqual, true, 'WASM betweennessApprox is deterministic across calls');
  assert.deepEqual(wave18BrowserAudit.wasmGraphAlgAudit.approxZero, [0, 0, 0, 0, 0], 'WASM betweennessApprox(0) returns finite zeros instead of NaN');
  assert.equal(wave18BrowserAudit.wasmGraphAlgAudit.approxZeroAllFinite, true);

  // 4b. Wave 23 Graph UX: single-repo #filter-repo-select hiding (verified on #/issues), right-click context menu + Escape isolation, What-If HUD + Cycle Navigator mutual exclusion + break-edge HUD, and Tarjan bridges.
  const issuesRepoAudit = await evaluate(page, `({
    view: ${app}.view,
    repoSelectVisible: (${visible})(document.querySelector('#filter-repo-select')),
    reposLen: (${app}.filterOptions?.repos || []).length,
  })`);
  assert.equal(issuesRepoAudit.view, 'issues', 'verify #filter-repo-select hiding while on #/issues');
  assert.equal(issuesRepoAudit.repoSelectVisible, false, 'single-repo export hides #filter-repo-select in Issues view');
  assert.equal(issuesRepoAudit.reposLen, 0, 'single-repo export has 0 multi-repo options');

  await click(page, 'a[href="#/graph"]');
  await waitFor(page, `${app}.view === 'graph' && window.location.hash === '#/graph' && ${app}.forceGraphReady && !${app}.forceGraphLoading && !!document.querySelector('#graph-container canvas')`, 'graph view ready for Wave 23 checks');
  const wave23GraphAudit = await evaluate(page, `(() => {
    const graphData = ${app}.forceGraphModule.getGraph().graphData();
    const hubLeaf = graphData.nodes.find(n => n.id === 'cap-hub-leaf');
    const authRoot = graphData.nodes.find(n => n.id === 'cap-auth-root');
    const bridgeLinks = graphData.links.filter(l => l.isBridge).map(l => [l.source?.id || l.source, l.target?.id || l.target]);
    const hasTwoNodeCycleBridge = bridgeLinks.some(([s, t]) =>
      (s === 'cap-auth-root' && t === 'cap-auth-ui') || (s === 'cap-auth-ui' && t === 'cap-auth-root') ||
      (s === 'cap-billing-a' && t === 'cap-billing-b') || (s === 'cap-billing-b' && t === 'cap-billing-a')
    );
    const colsCached1 = getOverviewColumnSet();
    const colsCached2 = getOverviewColumnSet();
    return {
      hasHubLeaf: Boolean(hubLeaf),
      hasAuthRoot: Boolean(authRoot),
      bridgeCount: bridgeLinks.length,
      hasTwoNodeCycleBridge,
      overviewColsCached: colsCached1 === colsCached2 && colsCached1.has('source_repo'),
      listenersBound: Boolean(${app}._graphListenersBound),
    };
  })()`);
  assert.equal(wave23GraphAudit.hasHubLeaf && wave23GraphAudit.hasAuthRoot, true, 'graph nodes loaded');
  assert.ok(wave23GraphAudit.bridgeCount >= 1, `expected >=1 Tarjan bridge cut-edge on capabilities graph (cap-hub-leaf -> cap-auth-root), got ${wave23GraphAudit.bridgeCount}`);
  assert.equal(wave23GraphAudit.hasTwoNodeCycleBridge, false, 'bidirectional 2-node cycles are never flagged as single-edge Tarjan bridges');
  assert.equal(wave23GraphAudit.overviewColsCached, true, 'PRAGMA table_info(issue_overview_mv) column set is cached per DB instance');
  assert.equal(wave23GraphAudit.listenersBound, true, 'graph document listeners guarded by _graphListenersBound');

  // Wave 24 Graph Filter HUD: toggle panel, assert #graph-filter-repo hidden on single-repo, filter status=closed, verify re-entry resets store.filters, reset!
  await click(page, '#graph-filter-toggle-btn');
  await waitFor(page, `(${visible})(document.querySelector('#graph-filter-hud'))`, 'Graph Filter HUD opens');
  assert.equal(await evaluate(page, `(${visible})(document.querySelector('#graph-filter-repo'))`), false, 'single-repo export hides #graph-filter-repo inside Graph Filter HUD');
  await evaluate(page, `(() => {
    const sel = document.getElementById('graph-filter-status');
    sel.value = 'closed';
    sel.dispatchEvent(new Event('change', { bubbles: true }));
  })()`);
  await waitFor(page, `${app}.graphVisibleTotal === 1 && ${app}.graphVisibleOpen === 0 && ${app}.graphVisibleClosed === 1`, 'Graph Filter HUD updates live visible node counts');
  // Navigating away from #/graph and back must reset store.filters in graph.js in lockstep with Alpine graphFilters!
  await click(page, 'a[href="#/issues"]');
  await waitFor(page, `${app}.view === 'issues'`, 'navigated to issues to test graph re-entry filter reset');
  await click(page, 'a[href="#/graph"]');
  await waitFor(page, `${app}.view === 'graph' && ${app}.forceGraphReady && !${app}.forceGraphLoading && ${app}.graphVisibleTotal === 6 && ${app}.forceGraphModule.getGraph().graphData().nodes.length === 6`, 'graph re-entry resets store.filters and renders all 6 nodes');
  if (await evaluate(page, `${app}.graphFiltersOpen`)) {
    await click(page, '#graph-filter-toggle-btn');
  }

  // Toggle Cycle Navigator -> #graph-cycle-hud shows cycle counter AND recommended break edge!
  await evaluate(page, `${app}.forceGraphModule.toggleCycleNavigator()`);
  await waitFor(page, `${app}.cycleNavigatorActive && Boolean(${app}.currentCycleBreakEdge?.from) && (${visible})(document.querySelector('#graph-cycle-hud'))`, 'Cycle Navigator HUD shows recommended break edge');
  const breakEdge = await evaluate(page, `JSON.parse(JSON.stringify(${app}.currentCycleBreakEdge))`);
  assert.ok(breakEdge.from && breakEdge.to, `recommended cycle break edge populated: ${JSON.stringify(breakEdge)}`);
  // Filtering out one endpoint hides currentCycleBreakEdge; clearing the filter restores it!
  await evaluate(page, `${app}.forceGraphModule.setFilter('search', ${JSON.stringify(breakEdge.from)})`);
  await waitFor(page, `${app}.cycleNavigatorActive && ${app}.currentCycleBreakEdge === null`, 'hiding break-edge endpoint clears currentCycleBreakEdge');
  await evaluate(page, `${app}.forceGraphModule.clearFilters()`);
  await waitFor(page, `${app}.cycleNavigatorActive && Boolean(${app}.currentCycleBreakEdge?.from)`, 'clearing graph filters restores currentCycleBreakEdge');

  // Dispatch bv-graph:nodeContextMenu with NaN & huge coordinates -> clamps safely; Escape closes ONLY context menu, keeping Cycle Navigator intact!
  await evaluate(page, `(() => {
    const node = ${app}.forceGraphModule.getGraph().graphData().nodes.find(n => n.id === 'cap-auth-root');
    document.dispatchEvent(new CustomEvent('bv-graph:nodeContextMenu', { detail: { node, x: NaN, y: Infinity } }));
  })()`);
  await waitFor(page, `${app}.graphContextMenu.open && Number.isFinite(${app}.graphContextMenu.x) && Number.isFinite(${app}.graphContextMenu.y)`, 'NaN context menu coords safely fall back to finite clamped position');
  await evaluate(page, `document.getElementById('graph-container')?.dispatchEvent(new WheelEvent('wheel', { deltaY: 120, bubbles: true }))`);
  await waitFor(page, `!${app}.graphContextMenu.open`, 'canvas wheel zoom dismisses open context menu');
  await evaluate(page, `(() => {
    const node = ${app}.forceGraphModule.getGraph().graphData().nodes.find(n => n.id === 'cap-auth-root');
    document.dispatchEvent(new CustomEvent('bv-graph:nodeContextMenu', { detail: { node, x: 99999, y: 99999 } }));
  })()`);
  await waitFor(page, `${app}.graphContextMenu.open && (${visible})(document.querySelector('#graph-context-menu'))`, 'right-click context menu opens');
  const menuPos = await evaluate(page, `({ x: ${app}.graphContextMenu.x, y: ${app}.graphContextMenu.y, w: window.innerWidth, h: window.innerHeight })`);
  assert.ok(menuPos.x >= 8 && menuPos.x < menuPos.w && menuPos.y >= 8 && menuPos.y < menuPos.h, `context menu clamped inside viewport: ${JSON.stringify(menuPos)}`);
  await key(page, 'Escape');
  await waitFor(page, `!${app}.graphContextMenu.open`, 'Escape closes context menu');
  assert.equal(await evaluate(page, `${app}.cycleNavigatorActive`), true, 'Escape on context menu does not leak to tear down Cycle Navigator');

  // Re-open context menu -> click What-If Close -> clears Cycle Navigator & activates #graph-whatif-hud with synced graphWhatIfBatchIds!
  await evaluate(page, `(() => {
    const node = ${app}.forceGraphModule.getGraph().graphData().nodes.find(n => n.id === 'cap-auth-root');
    document.dispatchEvent(new CustomEvent('bv-graph:nodeContextMenu', { detail: { node, x: 200, y: 200 } }));
  })()`);
  await waitFor(page, `${app}.graphContextMenu.open && (${visible})(document.querySelector('#graph-context-menu'))`, 'right-click context menu reopens');
  await click(page, '#graph-context-menu button span', 'What-If Close');
  await waitFor(page, `!${app}.graphContextMenu.open && ${app}.graphWhatIfActive && !${app}.cycleNavigatorActive && (${visible})(document.querySelector('#graph-whatif-hud'))`, 'What-If HUD opens from context menu and clears competing Cycle Navigator');
  assert.deepEqual(await evaluate(page, `[...${app}.graphWhatIfBatchIds]`), ['cap-auth-root'], 'graphWhatIfBatchIds stays synced with active What-If');
  // Calling no-op animateCriticalPath on cyclic graph must NOT tear down active What-If highlight!
  await evaluate(page, `${app}.forceGraphModule.animateCriticalPath(false)`);
  assert.equal(await evaluate(page, `${app}.graphWhatIfActive`), true, 'no-op animateCriticalPath on cyclic graph preserves active What-If overlay');
  await click(page, '#graph-whatif-hud button', 'Reset');
  await waitFor(page, `!${app}.graphWhatIfActive && ${app}.graphWhatIfBatchIds.length === 0`, 'What-If HUD Reset clears active simulation and batch queue');

  // 5. Error Modal runErrorAction(action) recovery handler ({ action }, { handler }, { url }).
  await evaluate(page, `(() => {
    window.__errorRecovered = [];
    window.__origOpen = window.open;
    window.open = (url, target, features) => { window.__errorRecovered.push([url, target, features].join('|')); return null; };
    showError({
      title: 'Synthetic Recovery Check',
      message: 'Verify runErrorAction supports action, handler, and url',
      dismissible: true,
      actions: [
        { label: 'Action Recovery', primary: true, action: () => window.__errorRecovered.push('action') },
        { label: 'Handler Recovery', handler: () => window.__errorRecovered.push('handler') },
        { label: 'URL Recovery', url: './index.html#recovered' },
      ],
    });
    ${app}.globalError = ERROR_STATE.error;
  })()`);
  await click(page, 'button', 'Action Recovery');
  await click(page, 'button', 'Handler Recovery');
  await click(page, 'button', 'URL Recovery');
  await click(page, 'button', 'Dismiss');
  await waitFor(page, `!${app}.globalError`, 'Dismiss closes error modal');
  const recovered = await evaluate(page, `(() => { window.open = window.__origOpen; return window.__errorRecovered; })()`);
  assert.deepEqual(recovered, ['action', 'handler', './index.html#recovered|_blank|noopener,noreferrer'], 'runErrorAction executes action, handler, and url recovery');

  await capture(page, 'capabilities');
  clean(page);
  assert.deepEqual(
    records.filter(r => r.page?.startsWith(page.name) && (
      (r.method === 'Network.responseReceived' && r.params.response.status >= 400) ||
      (r.method === 'Runtime.consoleAPICalled' && r.params.type === 'error' &&
        !String(r.params.args?.[0]?.value || '').includes('Synthetic Recovery Check'))
    )),
    [],
    `${page.name}: zero HTTP 4xx/5xx or unexpected console.error events`
  );
  console.log(`PASS: ${page.name} title/subtitle, charts/heatmap, labels, multi-word lexical+PageRank+FTS fields search, cycle navigator, eigenvector influencers, sizeMetric, galaxy/time-travel/arrow-keys, HITS/slack/THEME.priority, and error recovery`);
}

async function standaloneGraphJourney(page) {
  await waitFor(page, `typeof Graph !== 'undefined' && typeof applyFilters === 'function' && typeof computeNodeColor === 'function' && DATA.nodes.length === 6`, 'standalone interactive HTML graph booted');
  const adversarialMarkdown = [
    '> Quote line',
    '',
    'Inline: `a < b && c != "x"` and **bold**',
    '',
    '```go',
    'fmt.Println("safe")',
    '```',
    '',
    '| ColA | ColB |',
    '| :--- | ---: |',
    '| left | right |',
    '',
    '[SafeDocs](https://example.com/docs "DocsTitle")',
    '[bad-js](javascript:window.__js_xss=1)',
    '[tab-js](java&#x09;script:window.__tab_xss=1)',
    '[proto-rel](//evil.example/steal)',
    '[backslash](/\\evil.example/steal)',
    '<img src=x onerror="window.__img_xss=1">',
    '<svg><a id="animlink" href="https://example.com"><text y="20">ClickMe</text><set attributeName="href" to="javascript:window.__svg_set_xss=1"/></a></svg>',
    '<svg><animate attributeName="href" values="javascript:window.__svg_anim_xss=1"/></svg>',
    '<math><mtext><script>window.__math_xss=1</script></mtext></math>',
    '<template><script>window.__tmpl_xss=1</script></template>',
    '<button formaction="javascript:window.__form_xss=1">Submit</button>',
    '<a id="DATA" name="m-slack" style="position:fixed" onclick="window.__click_xss=1" href="https://example.com/clobber">Clobber</a>',
  ].join('\n');
  const initialAudit = await evaluate(page, `(async () => {
    const blockedNode = DATA.nodes.find(n => n.id === 'cap-hub-leaf');
    const closedNode = DATA.nodes.find(n => n.id === 'cap-closed-prereq');
    populatePanelContent('docked-', closedNode);
    const closedBadges = [...document.querySelectorAll('#docked-badges .badge')].map(el => el.textContent.trim());
    populatePanelContent('docked-', blockedNode);
    const blockedBadges = [...document.querySelectorAll('#docked-badges .badge')].map(el => el.textContent.trim());
    const dockedMetricsText = document.getElementById('docked-metrics')?.textContent || '';
    const statCritical = document.getElementById('stat-critical')?.textContent.trim() || '';

    const frag = renderSafeMarkdownFragment(${JSON.stringify(adversarialMarkdown)});
    const isDocFragment = frag instanceof DocumentFragment;
    const descEl = document.getElementById('docked-description-content');
    descEl.replaceChildren(frag);
    await new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r)));
    const mdHtml = descEl.innerHTML;
    const forbiddenTagCount = descEl.querySelectorAll('svg, math, set, animate, script, template, iframe, object, embed, form, input, button, textarea, select, style, link, meta, base, img').length;
    const nonXhtmlCount = [...descEl.querySelectorAll('*')].filter(el => el.namespaceURI !== 'http://www.w3.org/1999/xhtml').length;
    const unsafeAttrCount = [...descEl.querySelectorAll('*')].filter(el =>
      el.hasAttribute('id') || el.hasAttribute('name') || el.hasAttribute('style') ||
      [...el.attributes].some(a => a.name.toLowerCase().startsWith('on') || a.name.toLowerCase() === 'formaction' || a.name.toLowerCase() === 'srcdoc')
    ).length;
    const anchorAudit = [...descEl.querySelectorAll('a')].map(a => ({
      text: a.textContent.trim(),
      href: a.getAttribute('href'),
      rel: a.getAttribute('rel'),
      target: a.getAttribute('target'),
    }));
    const xssGlobalsTriggered = Boolean(
      window.__js_xss || window.__tab_xss || window.__img_xss || window.__svg_set_xss ||
      window.__svg_anim_xss || window.__math_xss || window.__tmpl_xss || window.__form_xss || window.__click_xss
    );
    const dataClobbered = !Array.isArray(DATA?.nodes);

    toggleLightMode();
    const lightOn = document.body.classList.contains('light-mode');
    toggleLightMode();
    const lightOff = !document.body.classList.contains('light-mode');

    localStorage.setItem('bv-graph-layout', 'td');
    applyGraphDagMode('td');

    return {
      dataHasCycles: DATA.has_cycles,
      dataSlackAvailable: DATA.slack_available,
      blockedSlackAvailable: blockedNode?.slack_available,
      closedSlackAvailable: closedNode?.slack_available,
      blockedNodeBlockerCount: blockedNode?.blockerCount || 0,
      blockedNodeColor: computeNodeColor(blockedNode),
      expectedBlockedColor: STATUS_COLORS.blocked,
      closedBadges,
      blockedBadges,
      dockedMetricsText,
      statCritical,
      isDocFragment,
      mdHtml,
      forbiddenTagCount,
      nonXhtmlCount,
      unsafeAttrCount,
      anchorAudit,
      xssGlobalsTriggered,
      dataClobbered,
      lightOn,
      lightOff,
    };
  })()`);
  assert.equal(initialAudit.dataHasCycles, true, 'cyclic standalone graph sets DATA.has_cycles=true');
  assert.equal(initialAudit.dataSlackAvailable, false, 'cyclic standalone graph sets DATA.slack_available=false');
  assert.equal(initialAudit.blockedSlackAvailable, false, 'cyclic standalone graph node sets slack_available=false');
  assert.equal(initialAudit.closedSlackAvailable, false, 'closed standalone graph node sets slack_available=false');
  assert.equal(initialAudit.statCritical, '0', 'cyclic standalone graph reports 0 critical-path nodes');
  assert.equal(initialAudit.blockedNodeBlockerCount, 1, 'cap-hub-leaf counts only active blocker cap-auth-root, not cap-closed-prereq');
  assert.equal(initialAudit.blockedNodeColor, initialAudit.expectedBlockedColor, 'active node with blockers colors with STATUS_COLORS.blocked');
  assert.equal(initialAudit.closedBadges.includes('Critical Path'), false, 'closed node never receives Critical Path badge');
  assert.equal(initialAudit.blockedBadges.includes('Critical Path'), false, 'cyclic active node never receives false Critical Path badge');
  assert.ok(initialAudit.dockedMetricsText.includes('N/A (cycle)'), `cyclic standalone graph shows N/A (cycle) slack: ${initialAudit.dockedMetricsText}`);
  assert.equal(initialAudit.isDocFragment, true, 'renderSafeMarkdownFragment returns a live DocumentFragment');
  assert.equal(initialAudit.forbiddenTagCount, 0, 'zero forbidden/foreign/form/script/template/svg/math tags survive sanitizer');
  assert.equal(initialAudit.nonXhtmlCount, 0, 'all surviving Markdown elements belong strictly to XHTML namespace');
  assert.equal(initialAudit.unsafeAttrCount, 0, 'zero id, name, style, event-handler, or formaction attributes survive sanitizer');
  assert.equal(initialAudit.xssGlobalsTriggered, false, 'no XSS payload executed in live Chrome DOM');
  assert.equal(initialAudit.dataClobbered, false, 'DOM clobbering id="DATA" stripped cleanly');
  assert.deepEqual(
    initialAudit.anchorAudit.filter(a => a.href !== null),
    [
      { text: 'SafeDocs', href: 'https://example.com/docs', rel: 'noopener noreferrer', target: '_blank' },
      { text: 'Clobber', href: 'https://example.com/clobber', rel: 'noopener noreferrer', target: '_blank' },
    ],
    `only validated https:// hrefs survive with rel="noopener noreferrer" target="_blank": ${JSON.stringify(initialAudit.anchorAudit)}`
  );
  assert.ok(
    initialAudit.mdHtml.includes('<blockquote>') &&
    initialAudit.mdHtml.includes('a &lt; b &amp;&amp; c != "x"') &&
    !initialAudit.mdHtml.includes('&amp;lt;') &&
    initialAudit.mdHtml.includes('class="language-go"') &&
    initialAudit.mdHtml.includes('<table>'),
    `renderSafeMarkdownFragment preserves blockquotes, code, language-* class, and tables without double-escaping: ${initialAudit.mdHtml}`
  );
  assert.ok(!initialAudit.mdHtml.includes('onerror') && !initialAudit.mdHtml.includes('javascript:') && !initialAudit.mdHtml.includes('evil.example'),
    `renderSafeMarkdownFragment strips XSS attributes, javascript: schemes, and protocol-relative/backslash URLs: ${initialAudit.mdHtml}`);
  assert.equal(initialAudit.lightOn, true, 'toggleLightMode enables body.light-mode');
  assert.equal(initialAudit.lightOff, true, 'toggleLightMode restores dark mode');
  await waitFor(page, `document.getElementById('view-mode')?.value === 'force' && localStorage.getItem('bv-graph-layout') === null && !Graph.dagMode() && (document.getElementById('toast')?.textContent || '').includes('Cannot apply DAG layout: dependency cycle detected')`, 'ForceGraph onDagError recovers from cycle, resets #view-mode to force, clears localStorage, and shows toast');

  // Wave 18 (bv-apal.86): Standalone HTML dagMode filters non-blocking links (related, discovered-from)
  // so bidirectional non-blocking links never trigger false cycle errors, and active PageRankRank is 0 for closed issues.
  const standaloneDagAndPrRankAudit = await evaluate(page, `(() => {
    const closedNode = DATA.nodes.find(n => n.id === 'cap-closed-prereq');
    const billingBNode = DATA.nodes.find(n => n.id === 'cap-billing-b');
    const rankOneActiveId = DATA.nodes.find(n => Number(n.pagerank_rank) === 1)?.id || '';
    const activeRanks = DATA.nodes.filter(n => n.status !== 'closed' && n.status !== 'tombstone').map(n => n.pagerank_rank).sort((a, b) => a - b);
    const savedLinks = DATA.links.map(l => ({ ...l }));
    const savedClosedPr = closedNode?.pagerank;
    const savedBillingBStatus = billingBNode?.status;
    try {
      // Planted high raw PageRank on closed node: #btn-top must still rank active pagerank_rank === 1 first and omit closed node!
      if (closedNode) closedNode.pagerank = 99.0;
      document.getElementById('btn-top').click();
      const topPanelIds = [...document.querySelectorAll('#top-nodes-panel .top-node-item')].map(el => el.dataset.id);
      document.getElementById('btn-top').click();

      // Also mark cap-billing-b closed temporarily so cap-closed-prereq <-> cap-billing-b forms a historical closed blocks cycle,
      // and include a blocking self-loop cap-hub-leaf -> cap-hub-leaf: DAG mode must exclude both!
      if (billingBNode) billingBNode.status = 'closed';
      const liveBillingB = Graph.graphData().nodes.find(n => n.id === 'cap-billing-b');
      if (liveBillingB) liveBillingB.status = 'closed';

      DATA.links = [
        { source: 'cap-hub-leaf', target: 'cap-auth-root', type: 'blocks', critical: false },
        { source: 'cap-hub-leaf', target: 'cap-hub-leaf', type: 'blocks', critical: false },
        { source: 'cap-closed-prereq', target: 'cap-billing-b', type: 'blocks', critical: false },
        { source: 'cap-billing-b', target: 'cap-closed-prereq', type: 'blocks', critical: false },
        { source: 'cap-auth-root', target: 'cap-auth-ui', type: 'related', critical: false },
        { source: 'cap-auth-ui', target: 'cap-auth-root', type: 'related', critical: false },
        { source: 'cap-billing-a', target: 'cap-auth-root', type: 'discovered-from', critical: false },
      ];
      const tdApplied = applyGraphDagMode('td');
      const dagModeNow = Graph.dagMode();
      const dagLinksCount = Graph.graphData().links.length;
      const dagHasNonBlocking = Graph.graphData().links.some(l => !isBlockingLinkType(l.type));
      const dagHasSelfLoop = Graph.graphData().links.some(l => {
        const s = typeof l.source === 'object' ? l.source.id : l.source;
        const t = typeof l.target === 'object' ? l.target.id : l.target;
        return s === t;
      });
      const forceApplied = applyGraphDagMode('force');
      const forceLinksCount = Graph.graphData().links.length;
      return {
        closedPrRank: closedNode?.pagerank_rank,
        rankOneActiveId,
        topPanelIds,
        activeRanks,
        tdApplied,
        dagErrorTriggered,
        dagModeNow,
        dagLinksCount,
        dagHasNonBlocking,
        dagHasSelfLoop,
        forceApplied,
        forceLinksCount,
      };
    } finally {
      if (closedNode) closedNode.pagerank = savedClosedPr;
      if (billingBNode) billingBNode.status = savedBillingBStatus;
      const liveBillingB = Graph.graphData().nodes.find(n => n.id === 'cap-billing-b');
      if (liveBillingB) liveBillingB.status = savedBillingBStatus;
      DATA.links = savedLinks;
      applyGraphDagMode('force');
    }
  })()`);
  assert.equal(standaloneDagAndPrRankAudit.closedPrRank, 0, 'closed standalone node has pagerank_rank === 0');
  assert.equal(standaloneDagAndPrRankAudit.topPanelIds[0], standaloneDagAndPrRankAudit.rankOneActiveId, '#btn-top selects #1 active issue by pagerank_rank rather than closed hub');
  assert.equal(standaloneDagAndPrRankAudit.topPanelIds.includes('cap-closed-prereq'), false, '#btn-top excludes closed nodes with pagerank_rank === 0');
  assert.deepEqual(standaloneDagAndPrRankAudit.activeRanks, [1, 2, 3, 4, 5], 'active standalone nodes receive contiguous PageRankRank 1..5');
  assert.equal(standaloneDagAndPrRankAudit.tdApplied, true, 'applyGraphDagMode("td") succeeds when cycles are only on closed nodes, self-loops, or non-blocking links');
  assert.equal(standaloneDagAndPrRankAudit.dagErrorTriggered, false, 'closed cycles, self-loops, and related/discovered-from links never trigger onDagError');
  assert.equal(standaloneDagAndPrRankAudit.dagModeNow, 'td');
  assert.equal(standaloneDagAndPrRankAudit.dagLinksCount, 1, 'dagMode filters Graph.graphData().links to the 1 active non-self-loop blocking link');
  assert.equal(standaloneDagAndPrRankAudit.dagHasNonBlocking, false);
  assert.equal(standaloneDagAndPrRankAudit.dagHasSelfLoop, false);
  assert.equal(standaloneDagAndPrRankAudit.forceApplied, true);
  assert.equal(standaloneDagAndPrRankAudit.forceLinksCount, 7, 'returning to force mode restores all 7 links');

  // Verify unified applyFilters across status, label, priority, and search.
  const filterAudit = await evaluate(page, `(() => {
    document.getElementById('filter-status').value = 'closed';
    document.getElementById('filter-status').dispatchEvent(new Event('change'));
    const closedVisible = DATA.nodes.filter(n => currentVisibilityFilter(n)).map(n => n.id);

    document.getElementById('filter-status').value = 'all';
    document.getElementById('filter-status').dispatchEvent(new Event('change'));
    document.getElementById('filter-label').value = 'observability';
    document.getElementById('filter-label').dispatchEvent(new Event('change'));
    const labelVisible = DATA.nodes.filter(n => currentVisibilityFilter(n)).map(n => n.id);
    const visibleStatText = document.getElementById('stat-visible').textContent.trim();
    return { closedVisible, labelVisible, visibleStatText };
  })()`);
  assert.deepEqual(filterAudit.closedVisible, ['cap-closed-prereq']);
  assert.deepEqual(filterAudit.labelVisible, ['cap-hub-leaf']);
  assert.equal(filterAudit.visibleStatText, '1 visible');
  assert.equal(await evaluate(page, `document.getElementById('filter-repo')?.style.display`), 'none', 'single-repo standalone HTML keeps #filter-repo hidden');

  // Verify selectNode -> addToRecent + #btn-recent + #btn-reset closing #recent-panel.
  const recentAndPathAudit = await evaluate(page, `(() => {
    const graphNodes = Graph.graphData().nodes;
    const hub = graphNodes.find(n => n.id === 'cap-hub-leaf');
    const ui = graphNodes.find(n => n.id === 'cap-auth-ui');
    selectNode(hub);
    const sidebarSlackText = document.getElementById('m-slack')?.textContent.trim() || '';
    const sidebarSlackHighlighted = Boolean(document.getElementById('m-slack')?.classList.contains('highlight'));
    document.getElementById('btn-recent').click();
    const recentOpenAfterClick = document.getElementById('recent-panel').classList.contains('visible') &&
      document.getElementById('btn-recent').classList.contains('active');
    const recentIds = [...document.querySelectorAll('#recent-list .recent-item')].map(el => el.dataset.id);

    // Path Finder ON -> select two connected nodes -> heatmap preserves dimming -> Path Finder OFF resets dimming
    togglePathFinder();
    handleNodeClick(hub);
    const startAfterFirst = pathStart;
    handleNodeClick(ui);
    const pathLen = activePathNodes ? activePathNodes.size : 0;
    const highlightedLenOn = highlightedNodes.size;
    // Toggle heatmap ON while path is highlighted: unrelated node cap-billing-a must be dimmed hsla(..., 0.15)
    document.getElementById('btn-heatmap').click();
    const billingNode = DATA.nodes.find(n => n.id === 'cap-billing-a');
    const dimmedHeatmapColor = computeNodeColor(billingNode);
    document.getElementById('btn-heatmap').click();

    // Toggle Path Finder OFF -> must clear activePathNodes & highlightedNodes
    togglePathFinder();
    const highlightedLenAfterOff = highlightedNodes.size;
    const activePathAfterOff = activePathNodes;
    const billingColorAfterOff = computeNodeColor(billingNode);

    // Reset button closes #recent-panel and resets filters
    document.getElementById('btn-reset').click();
    const recentClosedAfterReset = !document.getElementById('recent-panel').classList.contains('visible') &&
      !document.getElementById('btn-recent').classList.contains('active');
    const allVisibleAfterReset = DATA.nodes.filter(n => currentVisibilityFilter(n)).length;

    return {
      sidebarSlackText,
      sidebarSlackHighlighted,
      recentOpenAfterClick,
      recentIds,
      startAfterFirst,
      pathLen,
      highlightedLenOn,
      dimmedHeatmapColor,
      highlightedLenAfterOff,
      activePathAfterOff,
      billingColorAfterOff,
      recentClosedAfterReset,
      allVisibleAfterReset,
    };
  })()`);
  assert.equal(recentAndPathAudit.sidebarSlackText, 'N/A (cycle)', 'selectNode sets #m-slack to N/A (cycle) when slack_available=false');
  assert.equal(recentAndPathAudit.sidebarSlackHighlighted, false, 'selectNode never adds .highlight to #m-slack when slack_available=false');
  assert.equal(recentAndPathAudit.recentOpenAfterClick, true);
  assert.deepEqual(recentAndPathAudit.recentIds, ['cap-hub-leaf']);
  assert.equal(recentAndPathAudit.startAfterFirst, 'cap-hub-leaf');
  assert.equal(recentAndPathAudit.pathLen, 3, 'bfsPath highlights 3-node path cap-hub-leaf -> cap-auth-root -> cap-auth-ui');
  assert.equal(recentAndPathAudit.highlightedLenOn, 3);
  assert.match(recentAndPathAudit.dimmedHeatmapColor, /^hsla\(.*0\.15\)$/, 'heatmap mode preserves path dimming');
  assert.equal(recentAndPathAudit.highlightedLenAfterOff, 0, 'toggling Path Finder OFF clears highlightedNodes dimming');
  assert.equal(recentAndPathAudit.activePathAfterOff, null, 'toggling Path Finder OFF clears activePathNodes');
  assert.equal(recentAndPathAudit.billingColorAfterOff, '#ef4444', 'un-dimmed blocked cycle node restores full STATUS_COLORS.blocked');
  assert.equal(recentAndPathAudit.recentClosedAfterReset, true, '#btn-reset closes #recent-panel and clears .active on #btn-recent');
  assert.equal(recentAndPathAudit.allVisibleAfterReset, 6, '#btn-reset restores all 6 nodes');

  const standaloneThemeAndMenuAudit = await evaluate(page, `(() => {
    const sampleLink = Graph.graphData().links.find(l => !l.critical) || Graph.graphData().links[0];
    const darkLinkColor = computeLinkColor(sampleLink);
    toggleLightMode();
    const lightLinkColor = computeLinkColor(sampleLink);
    toggleLightMode();
    const hub = Graph.graphData().nodes[0];
    showContextMenu(hub, { clientX: 99999, clientY: 99999 });
    const menu = document.getElementById('context-menu');
    const rect = menu.getBoundingClientRect();
    const clampedInsideViewport = rect.right <= window.innerWidth && rect.bottom <= window.innerHeight && rect.left >= 0 && rect.top >= 0;
    hideContextMenu();

    // Verify closed-node Cut Vertex badge suppression + ephemeral/pinned/template withholding
    const closedCutProbe = { id: 'probe-closed-cut', title: 'Closed Cut', status: 'closed', priority: 1, type: 'task', is_articulation: true, slack_available: false };
    const openCutProbe = { id: 'probe-open-cut', title: 'Open Cut', status: 'open', priority: 1, type: 'task', is_articulation: true, slack_available: false };
    populatePanelContent('docked-', closedCutProbe);
    const closedCutBadges = [...document.querySelectorAll('#docked-badges .badge')].map(el => el.textContent.trim());
    populatePanelContent('docked-', openCutProbe);
    const openCutBadges = [...document.querySelectorAll('#docked-badges .badge')].map(el => el.textContent.trim());

    const ephProbe = { id: 'probe-eph', status: 'open', priority: 1, ephemeral: true };
    const pinProbe = { id: 'probe-pin', status: 'open', priority: 1, pinned: true };
    const tplProbe = { id: 'probe-tpl', status: 'open', priority: 1, is_template: true };
    const readyProbe = { id: 'probe-ready', status: 'open', priority: 1, ephemeral: false };

    return {
      darkLinkColor,
      lightLinkColor,
      clampedInsideViewport,
      closedCutHasBadge: closedCutBadges.includes('Cut Vertex'),
      openCutHasBadge: openCutBadges.includes('Cut Vertex'),
      withheldActionable: [isNodeActionable(ephProbe), isNodeActionable(pinProbe), isNodeActionable(tplProbe), isNodeActionable(readyProbe)],
      withheldColors: [computeNodeBaseColor(ephProbe), computeNodeBaseColor(pinProbe), computeNodeBaseColor(tplProbe), computeNodeBaseColor(readyProbe)],
    };
  })()`);
  assert.notEqual(standaloneThemeAndMenuAudit.darkLinkColor, standaloneThemeAndMenuAudit.lightLinkColor, 'standalone graph switches default link color in light mode');
  assert.equal(standaloneThemeAndMenuAudit.clampedInsideViewport, true, 'standalone graph clamps #context-menu inside viewport bounds');
  assert.equal(standaloneThemeAndMenuAudit.closedCutHasBadge, false, 'standalone graph suppresses Cut Vertex badge on closed nodes even if is_articulation=true');
  assert.equal(standaloneThemeAndMenuAudit.openCutHasBadge, true, 'standalone graph renders Cut Vertex badge on active articulation nodes');
  assert.deepEqual(standaloneThemeAndMenuAudit.withheldActionable, [false, false, false, true], 'standalone graph withholds ephemeral, pinned, and template open nodes from actionability');
  assert.deepEqual(standaloneThemeAndMenuAudit.withheldColors, ['#94a3b8', '#94a3b8', '#94a3b8', '#22c55e'], 'standalone graph colors withheld nodes muted slate (#94a3b8) instead of open green (#22c55e)');

  // Wave 25: Standalone Interactive HTML Graph Cycle Inspector, What-If Cascade, and 4-Tab Triage Panel
  const wave25StandaloneAudit = await evaluate(page, `(() => {
    const cyclesCount = Array.isArray(DATA.cycles) ? DATA.cycles.length : 0;
    const cycleBreaks = Array.isArray(DATA.cycle_breaks) ? DATA.cycle_breaks : [];
    const cycleBreaksCount = cycleBreaks.length;
    const allBreaksNonSelfLoop = cycleBreaks.every(b => b && b.from && b.to && b.from !== b.to);
    const linkPairs = new Set(DATA.links.map(l => (typeof l.source === 'object' ? l.source.id : l.source) + '\\0' + (typeof l.target === 'object' ? l.target.id : l.target)));
    const allBreaksRealLinks = cycleBreaks.every(b => linkPairs.has(b.from + '\\0' + b.to));

    // Cycle Inspector: click #btn-cycles -> banner opens at [1/2] with exact non-doubled path -> [2/2] -> close
    document.getElementById('btn-cycles').click();
    const cycleBannerOpen = document.getElementById('cycle-banner').classList.contains('visible');
    const cycleLabel1 = document.getElementById('cycle-banner-text').textContent.trim();
    document.getElementById('btn-cycle-next').click();
    const cycleLabel2 = document.getElementById('cycle-banner-text').textContent.trim();
    document.getElementById('btn-cycle-close').click();
    const cycleBannerClosed = !document.getElementById('cycle-banner').classList.contains('visible');

    // Closed node context menu must hide #ctx-whatif and runWhatIfSimulation must ignore closed nodes
    const closedNode = Graph.graphData().nodes.find(n => n.id === 'cap-closed-prereq');
    showContextMenu(closedNode, { clientX: 200, clientY: 200 });
    const ctxWhatIfHiddenOnClosed = document.getElementById('ctx-whatif').style.display === 'none';
    hideContextMenu();
    runWhatIfSimulation('cap-closed-prereq');
    const closedWhatIfIgnored = !document.getElementById('whatif-banner').classList.contains('visible');

    // What-If Close Cascade: simulate closing cap-auth-root -> opens #whatif-banner, reset clears it
    runWhatIfSimulation('cap-auth-root');
    const whatIfVisible = document.getElementById('whatif-banner').classList.contains('visible');
    const whatIfSummary = document.getElementById('whatif-banner-text').textContent.trim();
    document.getElementById('btn-whatif-close').click();
    const whatIfCleared = !document.getElementById('whatif-banner').classList.contains('visible');

    // 4-Tab Triage Panel: open #triage-panel and verify tabs Top / Quick Wins / Blockers / Alerts
    document.getElementById('btn-triage').click();
    const triageOpen = document.getElementById('triage-panel').style.display !== 'none';
    const tabButtons = [...document.querySelectorAll('#triage-tabs .triage-tab-btn')];
    const tabKeys = tabButtons.map(b => b.dataset.tab);
    tabButtons.find(b => b.dataset.tab === 'alerts')?.click();
    const alertsTabActive = document.querySelector('#triage-tabs .triage-tab-btn[data-tab="alerts"]')?.classList.contains('active');
    document.getElementById('btn-triage').click();

    // Wave 26 B9-1: verify parent-child & withheld-status What-If accuracy on synthetic graph snapshot
    const origNodes = DATA.nodes;
    const origLinks = DATA.links;
    let b91ParentChildOk = false;
    try {
      DATA.nodes = [
        { id: 'X', title: 'Blocker', status: 'open', priority: 1 },
        { id: 'P', title: 'Pinned Parent', status: 'pinned', priority: 1, blocked_by: ['X'] },
        { id: 'C', title: 'Child of P', status: 'open', priority: 2, blocked_by: ['P'] },
        { id: 'D', title: 'Draft dependent', status: 'draft', priority: 2, blocked_by: ['X'] },
      ];
      DATA.links = [
        { source: 'P', target: 'X', type: 'blocks' },
        { source: 'C', target: 'P', type: 'parent-child' },
        { source: 'D', target: 'X', type: 'blocks' },
      ];
      const simX = simulateWhatIfClose('X');
      const simP = simulateWhatIfClose('P');
      const rbC = computeStandaloneRootBlockers('C');
      b91ParentChildOk =
        JSON.stringify(simX.direct) === JSON.stringify(['C']) &&
        JSON.stringify(simP.direct) === JSON.stringify(['C']) &&
        !simX.cascade.includes('P') &&
        !simX.cascade.includes('D') &&
        Boolean(rbC && rbC.roots.length === 1 && rbC.roots[0].id === 'X');
    } finally {
      DATA.nodes = origNodes;
      DATA.links = origLinks;
    }
    const openNodeHasETA = DATA.nodes.some(n => isActiveNodeStatus(n.status) && n.eta_days != null && Boolean(n.eta_date));
    const closedNodeNoETA = DATA.nodes.filter(n => !isActiveNodeStatus(n.status)).every(n => n.eta_days == null);

    // B11-4: Filtering out all What-If highlighted nodes clears stale banner, hidden selection, and dimming.
    selectNode(DATA.nodes.find(n => n.id === 'cap-auth-root'));
    runWhatIfSimulation('cap-auth-root');
    const stSel = document.getElementById('filter-status');
    stSel.value = 'closed';
    stSel.onchange({ target: stSel });
    const b114ClearedOverlay = selectedNode === null && highlightedNodes.size === 0 && !document.getElementById('whatif-banner')?.classList.contains('visible');
    stSel.value = 'all';
    stSel.onchange({ target: stSel });

    return {
      cyclesCount,
      cycleBreaksCount,
      allBreaksNonSelfLoop,
      allBreaksRealLinks,
      cycleBannerOpen,
      cycleLabel1,
      cycleLabel2,
      cycleBannerClosed,
      ctxWhatIfHiddenOnClosed,
      closedWhatIfIgnored,
      whatIfVisible,
      whatIfSummaryHasRoot: whatIfSummary.includes('cap-auth-root'),
      whatIfCleared,
      triageOpen,
      tabKeys,
      alertsTabActive,
      b91ParentChildOk,
      openNodeHasETA,
      closedNodeNoETA,
      b114ClearedOverlay,
    };
  })()`);
  assert.equal(wave25StandaloneAudit.cyclesCount, 2, 'standalone HTML DATA.cycles exports both active cycles');
  assert.ok(wave25StandaloneAudit.cycleBreaksCount >= 1, 'standalone HTML DATA.cycle_breaks exports ranked break edges');
  assert.equal(wave25StandaloneAudit.allBreaksNonSelfLoop, true, '2-node cycle breaks never emit phantom A -> A self-loops');
  assert.equal(wave25StandaloneAudit.allBreaksRealLinks, true, 'every cycle break recommendation corresponds to a real directed link');
  assert.equal(wave25StandaloneAudit.cycleBannerOpen, true, 'clicking #btn-cycles opens #cycle-banner');
  assert.ok(wave25StandaloneAudit.cycleLabel1.includes('[1/2]') && wave25StandaloneAudit.cycleLabel2.includes('[2/2]'),
    `Cycle Inspector steps [1/2] -> [2/2]: ${JSON.stringify(wave25StandaloneAudit)}`);
  assert.equal(/(cap-[a-z-]+) → \1/.test(wave25StandaloneAudit.cycleLabel1), false,
    `Cycle Inspector path must not double closing node: ${wave25StandaloneAudit.cycleLabel1}`);
  assert.equal(wave25StandaloneAudit.cycleBannerClosed, true, 'closing #btn-cycle-close hides #cycle-banner');
  assert.equal(wave25StandaloneAudit.ctxWhatIfHiddenOnClosed, true, '#ctx-whatif is hidden on closed nodes');
  assert.equal(wave25StandaloneAudit.closedWhatIfIgnored, true, 'runWhatIfSimulation ignores closed nodes');
  assert.equal(wave25StandaloneAudit.whatIfVisible, true, 'runWhatIfSimulation opens #whatif-banner');
  assert.equal(wave25StandaloneAudit.whatIfSummaryHasRoot, true, '#whatif-banner-text names target node cap-auth-root');
  assert.equal(wave25StandaloneAudit.whatIfCleared, true, '#btn-whatif-close hides #whatif-banner');
  assert.equal(wave25StandaloneAudit.triageOpen, true, '#btn-triage opens #triage-panel');
  assert.deepEqual(wave25StandaloneAudit.tabKeys, ['recommendations', 'quick_wins', 'blockers', 'alerts'], '4-tab Triage panel renders Top/Quick Wins/Blockers/Alerts');
  assert.equal(wave25StandaloneAudit.alertsTabActive, true, 'clicking Alerts triage tab activates it');
  assert.equal(wave25StandaloneAudit.b91ParentChildOk, true, 'Wave 26 B9-1/W26-3 standalone parent-child What-If & root blocker accuracy verified');
  assert.equal(wave25StandaloneAudit.openNodeHasETA, true, 'Wave 26 W26-4 standalone open nodes carry ETA forecasts');
  assert.equal(wave25StandaloneAudit.closedNodeNoETA, true, 'Wave 26 W26-4 standalone closed nodes suppress ETA forecasts');
  assert.equal(wave25StandaloneAudit.b114ClearedOverlay, true, 'Wave 27 B11-4 standalone applyFilters clears hidden selection and zero-visible What-If overlay');

  await capture(page, 'standalone-graph');
  clean(page);
  console.log(`PASS: ${page.name} standalone HTML graph filters, blocked colors, active-only critical badge, addToRecent, heatmap dimming, and Path Finder ON/OFF reset`);
}

async function multiRepoJourney(page, standalonePage) {
  await waitFor(page, `typeof Alpine !== 'undefined' && !${app}.loading && ${app}.stats.total === 4 && ${app}.graphReady`, 'multi-repo bundle booted');
  await waitFor(page, '!!navigator.serviceWorker.controller', 'multi-repo worker controls page');
  await delay(400);
  await waitFor(page, `typeof Alpine !== 'undefined' && !${app}.loading && ${app}.stats.total === 4`, 'multi-repo app ready');

  await click(page, 'a[href="#/issues"]');
  await waitFor(page, `${app}.view === 'issues' && (${visible})(document.querySelector('#filter-repo-select'))`, 'multi-repo #filter-repo-select visible in Issues view');
  assert.deepEqual(await evaluate(page, `[...${app}.filterOptions.repos]`), ['api', 'web'], 'filterOptions.repos contains sorted multi-repo values');

  // Select 'api' via dropdown -> filters Issues, updates ?repo=api URL, displays removable #active-repo-filter-pill!
  await evaluate(page, `(() => {
    const sel = document.getElementById('filter-repo-select');
    sel.value = 'api';
    sel.dispatchEvent(new Event('change', { bubbles: true }));
  })()`);
  await waitFor(page, `${app}.filters.repo === 'api' && window.location.hash.includes('repo=api') && (${visible})(document.querySelector('#active-repo-filter-pill'))`, 'repo filter updates URL and active pill');
  await resultIDs(page, ['api-auth', 'api-health']);

  // Click removable Repo pill -> clears filter cleanly!
  await click(page, '#active-repo-filter-pill');
  await waitFor(page, `${app}.filters.repo === '' && !window.location.hash.includes('repo=')`, 'clicking Repo pill clears repo filter');
  await resultIDs(page, ['api-auth', 'api-health', 'web-login', 'web-settings']);

  // Click repo badge on issue card -> toggles filter on, clicking again toggles off!
  await click(page, 'span[title="Filter by repo: web"]', 'web');
  await waitFor(page, `${app}.filters.repo === 'web'`, 'repo badge click filters web');
  await resultIDs(page, ['web-login', 'web-settings']);
  await click(page, 'span[title="Filter by repo: web"]', 'web');
  await waitFor(page, `${app}.filters.repo === ''`, 'second repo badge click clears filter');

  // Graph View: #graph-filter-repo filters nodes & updates live counts, and graphDetailNode shows sourceRepo!
  await click(page, 'a[href="#/graph"]');
  await waitFor(page, `${app}.view === 'graph' && ${app}.forceGraphReady && !${app}.forceGraphLoading && !!document.querySelector('#graph-container canvas')`, 'multi-repo graph ready');
  await click(page, '#graph-filter-toggle-btn');
  await waitFor(page, `(${visible})(document.querySelector('#graph-filter-repo'))`, 'Graph Filter HUD shows #graph-filter-repo for multi-repo export');
  await evaluate(page, `(() => {
    const sel = document.getElementById('graph-filter-repo');
    sel.value = 'api';
    sel.dispatchEvent(new Event('change', { bubbles: true }));
  })()`);
  await waitFor(page, `${app}.graphVisibleTotal === 2 && ${app}.graphVisibleOpen === 1 && ${app}.graphVisibleClosed === 1`, 'Graph Filter HUD repo=api shows 2 api nodes (1 open, 1 closed)');
  await evaluate(page, `${app}.forceGraphModule.selectNode('api-auth')`);
  await waitFor(page, `${app}.graphDetailNode?.id === 'api-auth' && ${app}.graphDetailNode?.sourceRepo === 'api'`, 'graphDetailNode exposes sourceRepo');
  // Clicking Repo tile toggles repo filter off (was 'api') -> 4 nodes, then back on -> 2 nodes!
  await click(page, '#graph-detail-repo-btn');
  await waitFor(page, `${app}.graphFilters.repo === '' && ${app}.graphVisibleTotal === 4`, 'clicking graphDetailNode Repo tile clears active repo filter');
  await click(page, '#graph-detail-repo-btn');
  await waitFor(page, `${app}.graphFilters.repo === 'api' && ${app}.graphVisibleTotal === 2`, 'clicking graphDetailNode Repo tile re-applies repo filter');
  // Cross-view initForceGraph() resets stale graph filters when returning from another view.
  await click(page, 'a[href="#/issues"]');
  await waitFor(page, `${app}.view === 'issues' && !${app}.graphFiltersOpen`, 'leaving graph closes filter popover');
  await evaluate(page, `${app}.initForceGraph()`);
  await waitFor(page, `${app}.view === 'graph' && ${app}.graphFilters.repo === '' && ${app}.graphVisibleTotal === 4`, 'cross-view initForceGraph clears stale graph filters');
  // Batch What-If toggle lifecycle: 0 -> 1 -> 2 -> 1 -> 0.
  const batchSteps = await evaluate(page, `(async () => {
    const s = [];
    s.push(${app}.graphWhatIfBatchIds.length);
    await ${app}.toggleGraphWhatIfBatch('api-auth'); s.push(${app}.graphWhatIfBatchIds.length);
    await ${app}.toggleGraphWhatIfBatch('web-login'); s.push(${app}.graphWhatIfBatchIds.length);
    await ${app}.toggleGraphWhatIfBatch('api-auth'); s.push(${app}.graphWhatIfBatchIds.length);
    await ${app}.toggleGraphWhatIfBatch('web-login'); s.push(${app}.graphWhatIfBatchIds.length);
    return s;
  })()`);
  assert.deepEqual(batchSteps, [0, 1, 2, 1, 0], 'Batch What-If toggles cleanly through 0->1->2->1->0');
  // Verify getTriageAlerts() preserves multi-issue_ids, sorts critical->warning->info, and renders blue info badge.
  const alertOrderCheck = await evaluate(page, `(() => {
    const prev = ${app}.triageData;
    ${app}.triageData = {
      ...(prev || {}),
      alerts: [
        { type: 'high_impact_unblock', severity: 'info', message: 'Info item', issue_id: 'api-auth' },
        { type: 'abandoned_claim', severity: 'warning', message: 'Warn multi', issue_id: 'api-auth', issue_ids: ['api-auth', 'web-login'] },
        { type: 'cycle_detected', severity: 'critical', message: 'Crit item', issue_id: 'web-login' },
      ],
    };
    const res = ${app}.getTriageAlerts().map(a => ({ sev: a.severity, ids: a.issue_ids }));
    ${app}.triageData = prev;
    return res;
  })()`);
  assert.deepEqual(alertOrderCheck, [
    { sev: 'critical', ids: ['web-login'] },
    { sev: 'warning', ids: ['api-auth', 'web-login'] },
    { sev: 'info', ids: ['api-auth'] },
  ], 'getTriageAlerts sorts critical->warning->info and preserves deduplicated multi-issue_ids');

  // Wave 25: Sprints & Burndown View (#/sprints) + Dashboard Banner + Chart.js Canvas + Overdue Filter & Estimate Sort
  await waitFor(page, `${app}.hasSprints === true && (${visible})(document.getElementById('nav-sprints-link'))`, 'multi-repo export enables Sprints nav link');
  await click(page, '#nav-sprints-link');
  await waitFor(page, `${app}.view === 'sprints' && (${visible})(document.getElementById('sprints-view'))`, 'navigated to #/sprints view');
  await delay(150);
  const sprintViewAudit = await evaluate(page, `(() => {
    const canvas = document.getElementById('sprint-burndown-chart');
    const chart = typeof Chart !== 'undefined' && canvas ? Chart.getChart(canvas) : null;
    const text = document.getElementById('sprints-view')?.textContent || '';
    return {
      sprintId: ${app}.activeSprint?.id || '',
      hasChart: Boolean(chart),
      datasetCount: chart?.data?.datasets?.length || 0,
      firstTickLabel: chart?.data?.labels?.[0] || '',
      canvasWidth: canvas?.getBoundingClientRect?.().width || 0,
      hasCapacityText: text.includes('Graham Multi-Agent Capacity Bounds'),
      hasSprintBead: text.includes('api-auth') && text.includes('web-login'),
      hasUtcRangeText: text.includes('Oct 1, 2026') && text.includes('Oct 14, 2026'),
    };
  })()`);
  assert.equal(sprintViewAudit.sprintId, 'sprint-mr-1', '#/sprints activates sprint-mr-1');
  assert.equal(sprintViewAudit.hasChart, true, 'Chart.js renders #sprint-burndown-chart');
  assert.equal(sprintViewAudit.datasetCount, 2, 'sprint burndown chart renders Ideal + Actual datasets');
  assert.equal(sprintViewAudit.firstTickLabel, 'Oct 1', 'sprint burndown x-axis uses UTC date without negative-TZ off-by-one');
  assert.ok(sprintViewAudit.canvasWidth > 100, `sprint burndown canvas rendered non-zero width: ${sprintViewAudit.canvasWidth}`);
  assert.equal(sprintViewAudit.hasCapacityText, true, '#/sprints renders Graham Multi-Agent Capacity Bounds');
  assert.equal(sprintViewAudit.hasSprintBead, true, '#/sprints lists clickable sprint scope beads');
  assert.equal(sprintViewAudit.hasUtcRangeText, true, '#/sprints displays absolute UTC start/end date range');

  // Direct reload on #/sprints renders #sprint-burndown-chart after loading completes (not stuck on 0x0 hidden main)
  await reload(page);
  await waitFor(page, `typeof Alpine !== 'undefined' && !${app}.loading && ${app}.view === 'sprints' && ${app}.hasSprints`, 'direct #/sprints reload ready');
  await delay(150);
  const directSprintWidth = await evaluate(page, `(() => {
    const canvas = document.getElementById('sprint-burndown-chart');
    const chart = typeof Chart !== 'undefined' && canvas ? Chart.getChart(canvas) : null;
    return { hasChart: Boolean(chart), width: canvas?.getBoundingClientRect?.().width || 0 };
  })()`);
  assert.ok(directSprintWidth.hasChart && directSprintWidth.width > 100,
    `direct #/sprints reload renders visible Chart.js burndown: ${JSON.stringify(directSprintWidth)}`);

  // Mobile 360px direct boot onto #/sprints with hasSprints=true: 5-item bottom nav fits with zero horizontal overflow
  const mobileSprintPage = await openPage('multi-repo-mobile-360', 360, false, '/#/sprints');
  await waitFor(mobileSprintPage, `typeof Alpine !== 'undefined' && !${app}.loading && ${app}.view === 'sprints' && ${app}.hasSprints`, '360px mobile direct #/sprints initial boot');
  await waitFor(mobileSprintPage, '!!navigator.serviceWorker.controller', '360px mobile worker controls page');
  await delay(400);
  await waitFor(mobileSprintPage, `typeof Alpine !== 'undefined' && !${app}.loading && ${app}.view === 'sprints' && ${app}.hasSprints && !!document.querySelector('nav.fixed.bottom-0') && getComputedStyle(document.querySelector('nav.fixed.bottom-0')).position === 'fixed'`, '360px mobile direct #/sprints settled after SW activation');
  await delay(150);
  const mobileFiveNavAudit = await evaluate(mobileSprintPage, `(() => {
    const items = [...document.querySelectorAll('nav.fixed.bottom-0 a.nav-item-mobile')].filter(${visible});
    const rects = items.map(el => {
      const r = el.getBoundingClientRect();
      return { left: Math.round(r.left), right: Math.round(r.right), width: Math.round(r.width) };
    });
    const allWithin360 = rects.every(r => r.left >= 0 && r.right <= 361 && r.width >= 56);
    const noHorizOverflow = document.documentElement.scrollWidth <= 361;
    const canvas = document.getElementById('sprint-burndown-chart');
    const chart = typeof Chart !== 'undefined' && canvas ? Chart.getChart(canvas) : null;
    const offenders = noHorizOverflow ? [] : [...document.querySelectorAll('*')]
      .map(el => ({ tag: el.tagName, id: el.id, cls: String(el.className || '').slice(0, 40), right: Math.round(el.getBoundingClientRect().right) }))
      .filter(o => o.right > 361)
      .slice(0, 5);
    return {
      visibleCount: items.length,
      allWithin360,
      noHorizOverflow,
      hasChart: Boolean(chart),
      canvasWidth: Math.round(canvas?.getBoundingClientRect?.().width || 0),
      scrollWidth: document.documentElement.scrollWidth,
      rects,
      offenders,
    };
  })()`);
  clean(mobileSprintPage);
  await send('Target.closeTarget', { targetId: mobileSprintPage.target });
  assert.equal(mobileFiveNavAudit.visibleCount, 5, `expected 5 visible mobile nav items with hasSprints=true: ${JSON.stringify(mobileFiveNavAudit)}`);
  assert.equal(mobileFiveNavAudit.allWithin360, true, `all 5 mobile nav items fit within 360px: ${JSON.stringify(mobileFiveNavAudit)}`);
  assert.equal(mobileFiveNavAudit.noHorizOverflow, true, `no horizontal overflow at 360px on #/sprints: ${JSON.stringify(mobileFiveNavAudit)}`);
  assert.ok(mobileFiveNavAudit.hasChart && mobileFiveNavAudit.canvasWidth > 100 && mobileFiveNavAudit.canvasWidth <= 360,
    `360px mobile #/sprints burndown canvas fits viewport cleanly: ${JSON.stringify(mobileFiveNavAudit)}`);

  // Overdue quick filter & Estimate sort in #/issues
  await click(page, 'a[href="#/issues"]');
  await waitFor(page, `${app}.view === 'issues'`, 'navigated to #/issues for overdue/estimate check');
  await click(page, '#filter-overdue-btn');
  await waitFor(page, `${app}.filters.overdue === true`, 'Overdue quick filter active');
  await resultIDs(page, ['api-auth']);
  await click(page, '#filter-overdue-btn');
  await waitFor(page, `${app}.filters.overdue === false`, 'Overdue quick filter cleared');
  await evaluate(page, `(() => {
    const sel = document.getElementById('issues-sort-select');
    sel.value = 'estimate';
    sel.dispatchEvent(new Event('change', { bubbles: true }));
  })()`);
  await waitFor(page, `${app}.sort === 'estimate' && ${app}.issues[0]?.id === 'web-settings'`, 'Estimate sort places shortest-estimated 60m web-settings first');
  await resultIDs(page, ['web-settings', 'api-auth', 'web-login', 'api-health']);
  clean(page);

  // Standalone multi-repo HTML graph shows #filter-repo and filters DATA.nodes!
  await waitFor(standalonePage, `typeof Graph !== 'undefined' && DATA.nodes.length === 4`, 'multi-repo standalone HTML booted');
  const standaloneRepoAudit = await evaluate(standalonePage, `(() => {
    const sel = document.getElementById('filter-repo');
    const shown = Boolean(sel && sel.style.display !== 'none' && sel.options.length === 3);
    sel.value = 'web';
    sel.dispatchEvent(new Event('change', { bubbles: true }));
    const webCount = DATA.nodes.filter(n => currentVisibilityFilter(n)).length;
    document.getElementById('btn-reset').click();
    const resetCount = DATA.nodes.filter(n => currentVisibilityFilter(n)).length;
    return { shown, webCount, resetCount };
  })()`);
  assert.deepEqual(standaloneRepoAudit, { shown: true, webCount: 2, resetCount: 4 }, 'standalone HTML #filter-repo filters and resets multi-repo nodes');
  clean(standalonePage);
  console.log('PASS: multi-repo Issues dropdown/pill/badge/URL, Graph Filter HUD repo filter, and standalone HTML repo filter');
}
try {
  const stderr = fs.createWriteStream(path.join(artifacts, 'chrome.stderr'));
  chrome = spawn(browser, ['--headless=new', '--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage',
    '--no-first-run', '--disable-background-networking', '--disable-sync', '--disable-component-update',
    '--remote-debugging-port=0', `--user-data-dir=${path.join(artifacts, 'profile')}`,
    '--user-agent=beads_viewer-test (+https://github.com/Dicklesworthstone/beads_viewer)', 'about:blank'], { stdio: ['ignore', 'ignore', 'pipe'] });
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
  if (mode === 'journeys') {
    assert.ok(capabilitiesBundle, 'capabilities fixture export required for viewer capability journeys');
    activeBundle = capabilitiesBundle;
    const caps = await openPage('capabilities');
    await capabilitiesJourney(caps);
    await send('Target.closeTarget', { targetId: caps.target });
    const standalone = await openPage('standalone-graph', 1280, false, '/standalone-graph.html');
    await standaloneGraphJourney(standalone);
    await send('Target.closeTarget', { targetId: standalone.target });
    if (multiRepoBundle) {
      activeBundle = multiRepoBundle;
      const multi = await openPage('multi-repo');
      const multiStandalone = await openPage('multi-repo-standalone', 1280, false, '/standalone-graph.html');
      await multiRepoJourney(multi, multiStandalone);
      await send('Target.closeTarget', { targetId: multi.target });
      await send('Target.closeTarget', { targetId: multiStandalone.target });
    }
    activeBundle = bundle;
  }
  const desktop = await openPage('desktop');
  if (mode === 'graph-startup') {
    await graphStartupJourney(desktop);
    await send('Target.closeTarget',{targetId:desktop.target});
    await graphStartupJourney(await openPage('mobile-360',360));
  } else if (mode === 'timeline-performance') {
    await timelinePerformanceJourney(desktop);
    await send('Target.closeTarget',{targetId:desktop.target});
    await timelinePerformanceJourney(await openPage('mobile-360',360));
  } else if (mode === 'precomputed-metrics') {
    await precomputedMetricsJourney(desktop);
    await precomputedMetricsJourney(await openPage('mobile-360',360));
  } else if (mode === 'timeline-sprints') {
    await timelineSprintsJourney(desktop);
    await timelineSprintsJourney(await openPage('mobile-360',360));
  } else if (mode === 'timeline-animation') {
    await timelineAnimationJourney(desktop);
    await timelineAnimationJourney(await openPage('mobile-360',360));
  } else if (mode === 'timeline-removal') {
    await timelineRemovalJourney(desktop);
    await timelineRemovalJourney(await openPage('mobile-360',360));
  } else if (mode === 'timeline-baseline') {
    await timelineBaselineJourney(desktop);
    await timelineBaselineJourney(await openPage('mobile-360',360));
  } else if (mode === 'timeline-controls') {
    await timelineControlsJourney(desktop);
    await timelineControlsJourney(await openPage('mobile-360',360));
  } else if (mode === 'timeline') {
    await timelineJourney(desktop);
    await timelineJourney(await openPage('mobile-360',360));
  } else if (mode === 'history-loading') {
    await historyLoadingJourney(desktop);
    await historyLoadingJourney(await openPage('mobile-360',360));
  } else if (mode === 'graph-reload') {
    await graphReloadJourney(desktop);
    await graphReloadJourney(await openPage('mobile-360',360));
  } else if (mode === 'layout-seeds') {
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
    assert.equal(await evaluate(desktop, 'getHybridWasmStatus().reason'), 'Optional hybrid WASM module not bundled');
    assert.deepEqual(
      records.filter(r => (r.serverRequest && r.serverRequest.includes('bv_hybrid_scorer')) || (r.method === 'Network.responseReceived' && r.params?.response?.url?.includes('bv_hybrid_scorer'))),
      [],
      'optional bv_hybrid_scorer.js is gated by meta.has_hybrid_wasm with zero 404 requests'
    );
    assert.deepEqual(
      records.filter(r => {
        if (!['project', 'capabilities', 'standalone-graph', 'desktop', 'mobile-360'].some(prefix => r.page === prefix || r.page?.startsWith(prefix + ':'))) return false;
        if (r.method !== 'Network.responseReceived' || r.params.response.status < 400) return false;
        const pathname = new URL(r.params.response.url).pathname;
        // desktop/mobile-360 fixture intentionally contains sanitized <img src="x"> XSS probe and has no git repo for optional history.json
        if ((r.page?.startsWith('desktop') || r.page?.startsWith('mobile-360')) && (pathname === '/x' || pathname === '/data/history.json')) return false;
        return true;
      }),
      [],
      'zero unexpected HTTP 404/500 responses across project, capabilities, standalone-graph, desktop, and mobile journeys'
    );
    await key(desktop, 'Escape');
    await search(desktop, 'Orchid');
    await evaluate(desktop, `(() => {const e=[...document.querySelectorAll('select[x-model="searchMode"]')].find(${visible}); e.value='hybrid'; e.dispatchEvent(new Event('change',{bubbles:true}));})()`);
    await waitFor(desktop, `(() => { const el = [...document.querySelectorAll('select[x-model="searchPreset"]')].find(${visible}); return Boolean(el && el.offsetParent !== null && el.getBoundingClientRect().width > 0); })()`, 'hybrid preset visible');
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
    // Verify Quick Wins reason + Issue Modal triage recommendation & CSP-safe breakdown bars.
    const quickWinsReasonsOk = await evaluate(desktop, `${app}.quickWins.length > 0 && ${app}.quickWins.every(w => typeof w.reason === 'string' && w.reason.length > 0)`);
    assert.equal(quickWinsReasonsOk, true, 'Quick Wins populate real triage recommendation reasons');
    await click(desktop, '[aria-label^="View issue browser-root:"]');
    await waitFor(desktop, `${app}.selectedIssue?.id === 'browser-root'`, 'browser-root detail modal opened');
    const triageModalAudit = await evaluate(desktop, `(() => {
      const iss = ${app}.selectedIssue;
      const fills = [...document.querySelectorAll('.score-breakdown-fill')];
      return {
        reason: iss?.reason || '',
        reasonsCount: iss?.triage_reasons?.length || 0,
        barsCount: iss?.score_breakdown_bars?.length || 0,
        domFillsCount: fills.length,
        zeroInlineStyles: fills.every(el => !el.hasAttribute('style')),
      };
    })()`);
    assert.ok(triageModalAudit.reason.length > 0 && triageModalAudit.reasonsCount > 0, `Issue modal loads triage reasons: ${JSON.stringify(triageModalAudit)}`);
    assert.ok(triageModalAudit.barsCount >= 6, `getScoreBreakdownBars computes positive triage components: ${JSON.stringify(triageModalAudit)}`);
    assert.equal(triageModalAudit.domFillsCount, triageModalAudit.barsCount, 'Issue modal renders all positive score breakdown bars');
    assert.equal(triageModalAudit.zeroInlineStyles, true, 'Score breakdown bars use CSS classes with zero inline style attributes');
    clean(desktop);
    await key(desktop, 'Escape');
    await waitFor(desktop, `!${app}.selectedIssue`, 'Escape closes browser-root modal');
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
