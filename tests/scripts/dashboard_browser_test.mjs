// Explicit opt-in real Chromium journeys. No DOM substitutes or browser packages.
// Artifacts are consumed by the failing assertion/reviewer and retained externally.
// Usage: node tests/scripts/dashboard_browser_test.mjs CHROMIUM BUNDLE ARTIFACTS blocking-types
// Export a seven-task fixture: workflow-root is qa-review; workflow-{blocks,
// conditional,legacy,waits,related,closed} depend on it with types {blocks,
// conditional-blocks,"",waits-for,team-reference,waits-for}, respectively.
// The dependent statuses are open except workflow-closed, which is closed.
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import fs from "node:fs";
import http from "node:http";
import path from "node:path";

const [browser, bundle, artifacts, mode = "journeys", updatedBundle, projectBundle] =
  process.argv.slice(2);
assert.ok(browser && bundle && artifacts, "browser, bundle, artifacts required");
assert.ok(
  [
    "journeys",
    "offline-only",
    "blocking-types",
    "what-if",
    "hits",
    "readiness",
    "metric-visibility",
    "suggestion-visibility",
    "layout-seeds",
    "precomputed-metrics",
    "graph-startup",
    "graph-reload",
    "history-loading",
    "timeline",
    "timeline-controls",
    "timeline-baseline",
    "timeline-removal",
    "timeline-animation",
    "timeline-sprints",
    "timeline-performance",
  ].includes(mode),
  "unknown browser test mode",
);
fs.mkdirSync(artifacts, { recursive: true });
const records = [];
let brokenAsset = "",
  changedAsset = "",
  workerRevision = 0,
  chrome,
  server,
  socket;
let activeBundle = bundle;
let layoutVariant = null;
let historyVariant = null;
const mime = {
  ".html": "text/html",
  ".js": "text/javascript",
  ".css": "text/css",
  ".json": "application/json",
  ".wasm": "application/wasm",
  ".svg": "image/svg+xml",
};
server = http.createServer((req, res) => {
  res.on("finish", () =>
    records.push({
      serverRequest: req.url,
      status: res.statusCode,
      userAgent: req.headers["user-agent"],
    }),
  );
  const url = new URL(req.url, "http://localhost");
  let name = decodeURIComponent(url.pathname);
  if (name.endsWith("/")) name += "index.html";
  const file = path.resolve(activeBundle, "." + name);
  res.setHeader("Cache-Control", "no-store");
  res.setHeader("Cross-Origin-Opener-Policy", "same-origin");
  res.setHeader("Cross-Origin-Embedder-Policy", "require-corp");
  if (name === "/data/history.json" && mode === "history-loading" && historyVariant) {
    records.push({ historyRequest: req.url, variant: historyVariant });
    res.setHeader("Content-Type", "application/json");
    if (historyVariant === "stalled-headers") return;
    if (historyVariant === "stalled-body") {
      res.write('{"commits":');
      return;
    }
    if (historyVariant === "missing") {
      res.writeHead(404);
      res.end("missing optional history");
      return;
    }
    res.end(historyVariant === "malformed" ? "{" : '{"commits":[]}');
    return;
  }
  if (
    !file.startsWith(path.resolve(activeBundle) + path.sep) ||
    name === brokenAsset ||
    !fs.existsSync(file)
  ) {
    res.writeHead(404);
    res.end("Required file unavailable");
    return;
  }
  res.setHeader("Content-Type", mime[path.extname(file)] || "application/octet-stream");
  let body = fs.readFileSync(file);
  if (
    name === "/data/graph_layout.json" &&
    ["layout-seeds", "precomputed-metrics", "graph-startup"].includes(mode) &&
    layoutVariant
  ) {
    if (layoutVariant === "stalled-headers") return;
    if (layoutVariant === "stalled-body") {
      res.write('{"positions":');
      return;
    }
    if (layoutVariant === "missing") {
      res.writeHead(404);
      res.end("missing optional layout");
      return;
    }
    if (layoutVariant === "malformed") {
      res.end("{");
      return;
    }
    const layout = JSON.parse(body);
    if (layoutVariant === "partial") delete layout.positions[Object.keys(layout.positions)[0]];
    if (layoutVariant === "stale-edge") layout.links[0].reverse();
    if (layoutVariant === "stale-node") {
      const id = Object.keys(layout.positions)[0];
      layout.positions["absent-from-database"] = layout.positions[id];
      delete layout.positions[id];
    }
    if (layoutVariant === "invalid-coordinate")
      layout.positions[Object.keys(layout.positions)[0]][0] = "100";
    if (layoutVariant === "metric-missing")
      delete layout.centrality.pagerank[Object.keys(layout.centrality.pagerank)[0]];
    if (layoutVariant === "metric-invalid")
      layout.centrality.betweenness[Object.keys(layout.centrality.betweenness)[0]] = 999;
    if (layoutVariant === "metric-timeout") layout.centrality.status.PageRank.state = "timeout";
    if (layoutVariant === "metric-approximate")
      layout.centrality.status.Betweenness.reason = "approximate";
    if (layoutVariant === "metric-old-version") layout.centrality.version = 0;
    // A matching artifact with bogus metrics must never suppress real computation.
    for (const tuple of Object.values(layout.metrics)) tuple.fill(999);
    body = Buffer.from(JSON.stringify(layout));
  }
  if (name === changedAsset)
    body = Buffer.concat([body, Buffer.from("\n// changed after export\n")]);
  if (name === "/coi-serviceworker.js" && workerRevision) {
    body = Buffer.concat([body, Buffer.from(`\n// Browser update control ${workerRevision}\n`)]);
  }
  res.end(body);
});
await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
const port = server.address().port;
const origin = `http://127.0.0.1:${port}`;
const pending = new Map();
let sequence = 0;
const sessions = new Map();
const monitorErrors = [];
const delay = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
function send(method, params = {}, sessionId) {
  const id = ++sequence;
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => {
      pending.delete(id);
      reject(new Error(`CDP timeout: ${method}`));
    }, 30000);
    pending.set(id, { resolve, reject, timer, method, sessionId });
    socket.send(JSON.stringify({ id, method, params, ...(sessionId ? { sessionId } : {}) }));
  });
}
async function evaluate(page, expression) {
  const r = await send(
    "Runtime.evaluate",
    { expression, returnByValue: true, awaitPromise: true, userGesture: true },
    page.session,
  );
  if (r.exceptionDetails)
    throw new Error(r.exceptionDetails.exception?.description || r.exceptionDetails.text);
  return r.result.value;
}
async function waitFor(page, expression, label, ms = 25000) {
  const until = Date.now() + ms;
  let last;
  while (Date.now() < until) {
    try {
      if (await evaluate(page, expression)) return;
    } catch (err) {
      last = err.message;
    }
    await delay(100);
  }
  throw new Error(`Timed out: ${label}${last ? ": " + last : ""}`);
}
const app = `Alpine.$data(document.querySelector('[x-data="beadsApp()"]'))`;
const visible = `e => !!(e.getClientRects().length && getComputedStyle(e).visibility !== 'hidden')`;
async function capture(page, label) {
  if (page.worker) return;
  try {
    fs.writeFileSync(
      path.join(artifacts, `${page.name}-${label}.html`),
      await evaluate(page, "document.documentElement.outerHTML"),
    );
    const state = await evaluate(
      page,
      `typeof Alpine === 'undefined' ? null : (() => {const a=${app}; return JSON.parse(JSON.stringify({view:a.view, searchQuery:a.searchQuery, searchMode:a.searchMode, searchPreset:a.searchPreset, searchBackend:a.searchBackend, issues:a.issues, selectedIssue:a.selectedIssue, filters:a.filters, error:a.error, globalError:a.globalError}));})()`,
    );
    fs.writeFileSync(
      path.join(artifacts, `${page.name}-${label}.json`),
      JSON.stringify(state, null, 2),
    );
    const shot = await send(
      "Page.captureScreenshot",
      { format: "png", captureBeyondViewport: false },
      page.session,
    );
    fs.writeFileSync(
      path.join(artifacts, `${page.name}-${label}.png`),
      Buffer.from(shot.data, "base64"),
    );
  } catch (err) {
    records.push({ captureError: err.message, page: page.name });
  }
}
async function openPage(name, width = 1280, offline = false) {
  const { browserContextId } = await send("Target.createBrowserContext");
  await send("Browser.grantPermissions", {
    browserContextId,
    origin,
    permissions: ["clipboardReadWrite", "clipboardSanitizedWrite"],
  });
  const { targetId } = await send("Target.createTarget", { url: "about:blank", browserContextId });
  const { sessionId } = await send("Target.attachToTarget", { targetId, flatten: true });
  const page = {
    name,
    session: sessionId,
    target: targetId,
    context: browserContextId,
    errors: [],
    external: [],
  };
  sessions.set(sessionId, page);
  for (const domain of ["Runtime", "Page", "Network", "Log", "ServiceWorker"])
    await send(`${domain}.enable`, {}, sessionId);
  await send("Network.setCacheDisabled", { cacheDisabled: true }, sessionId);
  await send(
    "Emulation.setDeviceMetricsOverride",
    { width, height: 900, deviceScaleFactor: 1, mobile: width === 360 },
    sessionId,
  );
  await send("Fetch.enable", { patterns: [{ urlPattern: "*" }] }, sessionId);
  await send(
    "Target.setAutoAttach",
    { autoAttach: true, waitForDebuggerOnStart: true, flatten: true },
    sessionId,
  );
  if (offline) await setOffline(page, true);
  await send("Page.navigate", { url: origin + "/" }, sessionId);
  return page;
}
async function setOffline(page, offline) {
  // Workers are separate DevTools targets. Emulate offline on both the page
  // and every worker; otherwise a worker could silently repair a missing cache
  // entry over the network while its client appears offline.
  // Every context shares this test origin, so connectivity changes globally.
  for (const target of sessions.values()) target.offline = offline;
  records.push({ networkState: offline ? "offline" : "online", page: page.name });
  // Stop the actual server too: browser-process service-worker update checks
  // are not necessarily emitted by an attached page or worker target.
  if (offline && server.listening) {
    const closed = new Promise((resolve) => server.close(resolve));
    server.closeAllConnections();
    await closed;
  } else if (!offline && !server.listening) {
    await new Promise((resolve) => server.listen(port, "127.0.0.1", resolve));
  }
  assert.equal(server.listening, !offline, "actual origin listener matches network availability");
  records.push({ originListening: server.listening, page: page.name });
  const targets = [...sessions.values()].filter((p) => !p.detached);
  await Promise.all(targets.map((p) => emulateRequests(p, offline)));
  await Promise.all(
    targets
      .filter((p) => !p.worker)
      .map((p) =>
        send(
          "Network.overrideNetworkState",
          { offline, latency: 0, downloadThroughput: -1, uploadThroughput: -1 },
          p.session,
        ),
      ),
  );
  assert.equal(
    await evaluate(page, "navigator.onLine"),
    !offline,
    "browser exposes the actual emulated network state",
  );
}
function emulateRequests(page, offline) {
  // Keep request emulation separate from navigator state. Applying the old
  // combined command to another worker can reset a reloaded page's onLine.
  return send(
    "Network.emulateNetworkConditionsByRule",
    {
      offline,
      emulateOfflineServiceWorker: offline,
      matchedNetworkConditions: [
        { urlPattern: "", offline, latency: 0, downloadThroughput: -1, uploadThroughput: -1 },
      ],
    },
    page.session,
  );
}
async function ready(page) {
  await waitFor(
    page,
    `typeof Alpine !== 'undefined' && !${app}.loading && ${app}.stats.total === 4 && ${app}.graphReady`,
    "four exported issues and actual graph WASM ready",
  );
  assert.equal(await evaluate(page, `${app}.error || ${app}.globalError || null`), null);
}
async function reload(page) {
  await evaluate(page, "window.__journeyBeforeReload = true");
  // A hard reload deliberately bypasses the service worker in Chromium.
  // Ordinary reload with the HTTP cache disabled tests the offline contract.
  await send("Page.reload", { ignoreCache: false }, page.session);
  await waitFor(page, "!window.__journeyBeforeReload", "new document after reload");
  await ready(page);
  // Chrome's process-wide navigator override can reset on navigation. The
  // stopped origin plus worker request blocking is the offline proof.
  records.push({
    reloadedOnline: await evaluate(page, "navigator.onLine"),
    page: page.name,
    originListening: server.listening,
  });
  if (page.offline)
    assert.equal(server.listening, false, "offline reload succeeded while the origin was stopped");
}
async function click(page, selector, text) {
  const predicate =
    text === undefined
      ? visible
      : `e => (${visible})(e) && e.textContent.trim() === ${JSON.stringify(text)}`;
  await waitFor(
    page,
    `[...document.querySelectorAll(${JSON.stringify(selector)})].some(${predicate})`,
    `visible ${selector} ${text || ""}`,
  );
  const point = await evaluate(
    page,
    `(() => { const e = [...document.querySelectorAll(${JSON.stringify(selector)})].find(${predicate}); e.scrollIntoView({block:'center'}); const r=e.getBoundingClientRect(); return {x:r.x+r.width/2,y:r.y+r.height/2}; })()`,
  );
  assert.ok(
    point.x > 0 && point.x < (await evaluate(page, "innerWidth")),
    `${selector} is horizontally reachable`,
  );
  await waitFor(
    page,
    `(() => { const e=[...document.querySelectorAll(${JSON.stringify(selector)})].find(${predicate}); return e?.contains(document.elementFromPoint(${point.x},${point.y})); })()`,
    `uncovered ${selector}`,
  );
  for (const type of ["mousePressed", "mouseReleased"])
    await send(
      "Input.dispatchMouseEvent",
      { type, ...point, button: "left", clickCount: 1 },
      page.session,
    );
}
async function key(page, key, code = key) {
  const codes = {
    Enter: 13,
    Escape: 27,
    Home: 36,
    End: 35,
    ArrowLeft: 37,
    ArrowUp: 38,
    ArrowRight: 39,
    ArrowDown: 40,
  };
  for (const type of ["keyDown", "keyUp"])
    await send(
      "Input.dispatchKeyEvent",
      { type, key, code, windowsVirtualKeyCode: codes[key] || 0 },
      page.session,
    );
}
async function search(page, text) {
  await click(page, 'input[placeholder="Search issues..."]');
  await evaluate(
    page,
    `(() => { const e=[...document.querySelectorAll('input[placeholder="Search issues..."]')].find(${visible}); e.value=${JSON.stringify(text)}; e.dispatchEvent(new Event('input',{bubbles:true})); })()`,
  );
  await waitFor(
    page,
    `${app}.searchQuery === ${JSON.stringify(text)} && ${app}.view === 'issues'`,
    "search applied",
  );
  await delay(500);
}
function clean(page) {
  assert.deepEqual(monitorErrors, [], "browser/worker network monitor configured");
  assert.deepEqual(page.external, [], `${page.name}: external network attempted`);
  assert.deepEqual(page.errors, [], `${page.name}: uncaught exception/CSP refusal`);
}
async function resultIDs(page, expected) {
  await waitFor(
    page,
    `JSON.stringify([...document.querySelectorAll('[aria-label^="View issue "]')].filter(${visible}).map(e => e.getAttribute('aria-label').split(':')[0].slice(11)).sort()) === ${JSON.stringify(JSON.stringify([...expected].sort()))}`,
    `visible issue IDs ${expected}`,
  );
}

async function timelineSprintsJourney(page) {
  await ready(page);
  await waitFor(
    page,
    "!!navigator.serviceWorker.controller",
    "sprint fixture worker controls page",
  );
  await delay(500);
  await ready(page);
  await click(page, 'a[href="#/graph"]');
  await waitFor(page, `${app}.forceGraphReady && !${app}.forceGraphLoading`, "sprint graph loaded");
  await key(page, "t", "KeyT");
  const markers = ".timeline-sprints button";
  const texts = await evaluate(
    page,
    `[...document.querySelectorAll('${markers}')].map(e=>e.textContent)`,
  );
  assert.deepEqual(
    texts,
    ["Start: Review <em>phase</em>", "End: Review <em>phase</em>"],
    "only retained boundaries appear as literal text",
  );
  assert.equal(
    await evaluate(page, `document.querySelectorAll('.timeline-sprints em').length`),
    0,
    "sprint names are not interpreted as markup",
  );
  const state = `${app}.forceGraphModule.getTimeTravelState()`;
  await click(page, markers, texts[0]);
  assert.equal(
    await evaluate(page, `${state}.currentIdx`),
    2,
    "backdated source commit is selected by boundary time",
  );
  assert.equal(
    await evaluate(page, `document.querySelector('#tt-date').textContent`),
    "Sep 2, 2026",
  );
  await click(page, markers, texts[1]);
  assert.equal(await evaluate(page, `${state}.currentIdx`), 5);
  assert.equal(
    await evaluate(page, `document.querySelector('#tt-date').textContent`),
    "Sep 6, 2026",
    "date changes with boundary navigation",
  );
  await capture(page, "timeline-sprints");
  clean(page);
  console.log(
    `PASS: ${page.name} sprint boundaries, backdated chronology, date updates and literal names`,
  );
}

async function timelinePerformanceJourney(page) {
  const history = JSON.parse(fs.readFileSync(path.join(bundle, "data/history.json"), "utf8"));
  assert.equal(history.commits.length, 21, "twenty real recorded transitions");
  const readyLarge = `typeof Alpine !== 'undefined' && !${app}.loading && ${app}.stats.total === 1000 && ${app}.graphReady`;
  await waitFor(page, readyLarge, "1000 exported issues and actual graph WASM ready");
  await waitFor(
    page,
    "!!navigator.serviceWorker.controller",
    "performance fixture worker controls page",
  );
  await delay(500);
  await waitFor(page, readyLarge, "large fixture after worker activation");
  await click(page, 'a[href="#/graph"]');
  await waitFor(
    page,
    `${app}.forceGraphReady && !${app}.forceGraphLoading && ${app}.graphLoadingStage === null`,
    "large graph loaded",
  );
  const state = `${app}.forceGraphModule.getTimeTravelState()`;
  const priorWarmup = await evaluate(page, `${app}.forceGraphModule.getGraph().warmupTicks()`);
  await key(page, "t", "KeyT");
  await delay(500);
  await evaluate(page, `document.querySelector('#tt-speed').focus()`);
  await key(page, "End");
  await key(page, "ArrowUp");
  assert.equal(await evaluate(page, `${state}.speed`), 5);
  await evaluate(
    page,
    `(() => {
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
  })()`,
  );
  await send("Profiler.enable", {}, page.session);
  await send("Profiler.start", {}, page.session);
  await click(page, "#tt-play");
  await waitFor(
    page,
    `${state}.currentIdx===20 && !${state}.playing`,
    "complete 5x playback",
    10000,
  );
  await evaluate(
    page,
    `new Promise(resolve=>setTimeout(resolve,Math.max(0,window.__timelinePerf.ticks.at(-1).at+250-performance.now())))`,
  );
  const result = await evaluate(
    page,
    `(() => {const p=window.__timelinePerf;p.active=false;p.elapsed=performance.now()-p.started;return p;})()`,
  );
  const profile = await send("Profiler.stop", {}, page.session);
  fs.writeFileSync(
    path.join(artifacts, `${page.name}-timeline.cpuprofile`),
    JSON.stringify(profile.profile),
  );
  const sorted = result.frames.filter((n) => n >= 0).sort((a, b) => a - b);
  const percentile = (q) => sorted[Math.min(sorted.length - 1, Math.ceil(q * sorted.length) - 1)];
  const summary = {
    frames: sorted.length,
    p95: percentile(0.95),
    p99: percentile(0.99),
    max: sorted.at(-1),
    paints: result.paints,
    elapsed: result.elapsed,
  };
  records.push({ timelinePerformance: { result, summary }, page: page.name });
  console.log(`Timeline performance ${page.name}: ${JSON.stringify(summary)}`);
  assert.equal(result.ticks.length, 20, "all twenty transitions observed, no skipped steps");
  assert.ok(
    result.ticks.every((t, i) => t.index === i + 1 && t.nodes === (i % 2 === 0 ? 950 : 1000)),
    "every recorded closure/reopen applied",
  );
  assert.ok(
    sorted.length >= 120 && result.paints >= 1000,
    "measure actual rendering throughout playback",
  );
  // These are browser-animation bounds, not CLI/TUI latency or physical-device claims.
  assert.ok(
    summary.p95 <= 33.4 && summary.p99 <= 50,
    "1000-node playback frame intervals: p95 <=33.4ms, p99 <=50ms",
  );
  await click(page, ".timeline-close");
  assert.equal(
    await evaluate(page, `${app}.forceGraphModule.getGraph().warmupTicks()`),
    priorWarmup,
    "exit restores configured layout warmup",
  );
  clean(page);
  console.log(`PASS: ${page.name} 1000-node timeline playback frame budget`);
}

async function timelineAnimationJourney(page) {
  await ready(page);
  await waitFor(
    page,
    "!!navigator.serviceWorker.controller",
    "animation fixture worker controls page",
  );
  await delay(500);
  await ready(page);
  await click(page, 'a[href="#/graph"]');
  await waitFor(
    page,
    `${app}.forceGraphReady && !${app}.forceGraphLoading`,
    "animation graph loaded",
  );
  // Observe actual visible-canvas arcs without replacing the renderer or clock.
  await evaluate(
    page,
    `(() => {
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
  })()`,
  );
  const graph = `${app}.forceGraphModule.getGraph()`;
  const state = `${app}.forceGraphModule.getTimeTravelState()`;
  await key(page, "t", "KeyT");
  await delay(400);
  const appearing = await evaluate(page, "window.__timelinePaint");
  assert.ok(
    appearing.some((p) => p.alpha > 0 && p.alpha < 0.9),
    "actual canvas paints intermediate appearance alpha",
  );
  const fullRadius = Math.min(...appearing.filter((p) => p.alpha === 1).map((p) => p.r));
  assert.ok(Number.isFinite(fullRadius), "appearance reaches full size and opacity");
  assert.ok(
    appearing.some((p) => p.alpha < 1 && p.r > fullRadius * 1.01),
    "appearing node briefly pulses above settled size",
  );
  await evaluate(page, `window.__timelinePaint=[]; document.querySelector('#tt-slider').focus()`);
  await key(page, "ArrowRight");
  assert.equal(await evaluate(page, `${state}.currentIdx`), 1);
  assert.equal(
    await evaluate(page, `${graph}.graphData().nodes.some(n=>n.id==='browser-detail')`),
    false,
    "removed node immediately leaves interactive graph",
  );
  await delay(400);
  const disappearing = await evaluate(page, "window.__timelinePaint");
  assert.ok(
    disappearing.some((p) => p.alpha > 0 && p.alpha < 0.9 && p.r < fullRadius),
    "removed node actually fades and shrinks on canvas",
  );
  assert.equal(
    await evaluate(page, `${graph}.autoPauseRedraw()`),
    true,
    "redraw returns to idle policy",
  );
  await evaluate(page, "window.__timelinePaint=[]");
  await delay(150);
  assert.deepEqual(
    await evaluate(page, "window.__timelinePaint"),
    [],
    "no stale disappearing overlay",
  );
  // Reverse an unfinished appearance; it must fade from its current size.
  await key(page, "ArrowRight");
  await delay(70);
  await key(page, "ArrowLeft");
  await evaluate(page, "window.__timelinePaint=[]");
  await delay(300);
  const reversed = await evaluate(page, "window.__timelinePaint");
  assert.ok(
    reversed.some((p) => p.alpha > 0 && p.alpha < 0.9),
    "interrupted reverse scrub continues a partial fade",
  );
  assert.ok(
    reversed.every((p) => p.alpha < 0.9),
    "reverse scrub does not flash back to full opacity",
  );
  await key(page, "ArrowRight");
  await delay(50);
  await click(page, ".timeline-close");
  assert.equal(
    await evaluate(page, `${graph}.autoPauseRedraw()`),
    true,
    "exit cancels transition redraw",
  );
  await evaluate(page, `${graph}.autoPauseRedraw(false)`);
  await key(page, "t", "KeyT");
  await delay(300);
  assert.equal(
    await evaluate(page, `${graph}.autoPauseRedraw()`),
    false,
    "settling preserves an existing continuous-redraw policy",
  );
  await click(page, ".timeline-close");
  assert.equal(
    await evaluate(page, `${graph}.autoPauseRedraw()`),
    false,
    "exit preserves the previous redraw policy",
  );
  await evaluate(page, `${graph}.autoPauseRedraw(true)`);
  await send(
    "Emulation.setEmulatedMedia",
    { features: [{ name: "prefers-reduced-motion", value: "reduce" }] },
    page.session,
  );
  await evaluate(page, "window.__timelinePaint=[]");
  await key(page, "t", "KeyT");
  await delay(100);
  const reducedPaint = await evaluate(page, "window.__timelinePaint");
  assert.ok(
    reducedPaint.length > 0 && reducedPaint.every((p) => p.alpha === 1),
    "reduced motion paints at full opacity without intermediate appearance",
  );
  await evaluate(page, `window.__timelinePaint=[]; document.querySelector('#tt-slider').focus()`);
  await key(page, "ArrowRight");
  await evaluate(page, "window.__timelinePaint=[]");
  await delay(100);
  assert.deepEqual(
    await evaluate(page, "window.__timelinePaint"),
    [],
    "reduced motion skips disappearing overlay",
  );
  for (const preset of ["spread", "compact"]) {
    await evaluate(page, `${app}.forceGraphModule.applyPreset('${preset}')`);
    assert.equal(
      await evaluate(page, `${graph}.warmupTicks()`),
      0,
      "preset changes keep playback warmup disabled",
    );
    await click(page, ".timeline-close");
    assert.equal(
      await evaluate(page, `${graph}.warmupTicks()`),
      preset === "spread" ? 150 : 50,
      "exit restores the newly chosen standard or custom preset",
    );
    await key(page, "t", "KeyT");
  }
  records.push({
    timelineAnimation: { appearing, disappearing, reversed, fullRadius },
    page: page.name,
  });
  await capture(page, "timeline-animation");
  clean(page);
  console.log(`PASS: ${page.name} real canvas fade/shrink, cleanup and reduced motion`);
}

async function timelineRemovalJourney(page) {
  const history = JSON.parse(fs.readFileSync(path.join(bundle, "data/history.json")));
  assert.equal(history.commits.length, 7, "actual recorded deletion/reintroduction fixture");
  for (const i of [1, 4]) {
    assert.deepEqual(history.commits[i].beads_removed, ["browser-detail"]);
    assert.equal(history.commits[i].beads_closed, undefined, "removal is not completion");
  }
  await ready(page);
  await waitFor(
    page,
    "!!navigator.serviceWorker.controller",
    "removal fixture worker controls page",
  );
  await delay(500);
  await ready(page);
  await click(page, 'a[href="#/graph"]');
  await waitFor(
    page,
    `${app}.forceGraphReady && !${app}.forceGraphLoading`,
    "removal graph loaded",
  );
  const state = `${app}.forceGraphModule.getTimeTravelState()`;
  await key(page, "t", "KeyT");
  await waitFor(page, `${state}.active`, "keyboard enters removal timeline");
  await evaluate(page, `document.querySelector('#tt-slider').focus()`);
  const present = ["browser-detail", "browser-other", "browser-root"];
  const absent = ["browser-other", "browser-root"];
  const expected = [present, absent, present, absent, absent, absent, present];
  // Native range keys exercise both directions, including closed reintroduction.
  for (const indices of [
    [0, 1, 2, 3, 4, 5, 6],
    [5, 4, 3, 2, 1, 0],
  ]) {
    for (const i of indices) {
      if ((await evaluate(page, `${state}.currentIdx`)) !== i) {
        await key(page, indices[0] === 0 ? "ArrowRight" : "ArrowLeft");
      }
      assert.equal(await evaluate(page, `${state}.currentIdx`), i);
      assert.deepEqual(
        await evaluate(
          page,
          `${app}.forceGraphModule.getGraph().graphData().nodes.map(n=>n.id).sort()`,
        ),
        expected[i],
        `record ${i} visibility`,
      );
      const links = await evaluate(
        page,
        `${app}.forceGraphModule.getGraph().graphData().links.length`,
      );
      assert.equal(
        links,
        expected[i] === present ? 1 : 0,
        "absent nodes have no dangling dependency links",
      );
    }
  }
  await click(page, ".timeline-close");
  assert.equal(
    await evaluate(page, `${app}.forceGraphModule.getGraph().graphData().nodes.length`),
    4,
  );
  await capture(page, "timeline-removal");
  clean(page);
  console.log(
    `PASS: ${page.name} deletion, open/closed reintroduction, reopening and reverse replay`,
  );
}

async function timelineBaselineJourney(page) {
  const history = JSON.parse(fs.readFileSync(path.join(bundle, "data/history.json")));
  assert.equal(history.commits.length, 500, "real export reaches retained history limit");
  const baseline = ["browser-detail", "browser-root"];
  assert.deepEqual(history.initial_beads, baseline, "observed prior open issues seed baseline");
  await ready(page);
  await waitFor(
    page,
    "!!navigator.serviceWorker.controller",
    "baseline fixture worker controls page",
  );
  await delay(500);
  await ready(page);
  await click(page, 'a[href="#/graph"]');
  await waitFor(
    page,
    `${app}.forceGraphReady && !${app}.forceGraphLoading`,
    "baseline graph loaded",
  );
  const state = `${app}.forceGraphModule.getTimeTravelState()`;
  const nodes = `${app}.forceGraphModule.getGraph().graphData().nodes.map(n=>n.id).sort()`;
  await key(page, "t", "KeyT");
  await waitFor(page, `${state}.active`, "keyboard enters retained timeline");
  assert.equal(await evaluate(page, `${state}.currentIdx`), 0);
  assert.deepEqual(
    await evaluate(page, nodes),
    baseline,
    "first retained record includes older open issues, excludes closed and future creation",
  );
  await evaluate(page, `document.querySelector('#tt-slider').focus()`);
  await key(page, "End");
  assert.equal(await evaluate(page, `${state}.currentIdx`), 499);
  assert.deepEqual(
    await evaluate(page, nodes),
    ["browser-other", "browser-root"],
    "later creation and closure update baseline",
  );
  await key(page, "Home");
  assert.deepEqual(
    await evaluate(page, nodes),
    baseline,
    "rewinding reconstructs initial visibility",
  );
  await click(page, ".timeline-close");
  assert.deepEqual(
    await evaluate(page, nodes),
    ["browser-closed", "browser-detail", "browser-other", "browser-root"],
    "exit restores all current issues",
  );
  records.push({
    timelineBaseline: { initial: history.initial_beads, commits: history.commits.length },
    page: page.name,
  });
  await capture(page, "timeline-baseline");
  clean(page);
  console.log(`PASS: ${page.name} retained baseline, later creation/closure, rewind and exit`);
}

async function timelineControlsJourney(page) {
  await ready(page);
  await waitFor(
    page,
    "!!navigator.serviceWorker.controller",
    "controls fixture worker controls page",
  );
  await delay(500);
  await ready(page);
  await click(page, 'a[href="#/graph"]');
  await waitFor(
    page,
    `${app}.forceGraphReady && !${app}.forceGraphLoading`,
    "controls graph loaded",
  );
  const state = `${app}.forceGraphModule.getTimeTravelState()`;
  await key(page, "t", "KeyT");
  await evaluate(page, `document.querySelector('#tt-slider').focus()`);
  await key(page, "End");
  assert.equal(
    await evaluate(page, `${state}.currentIdx`),
    3,
    "native range End reaches final commit",
  );
  await key(page, "Home");
  assert.equal(
    await evaluate(page, `${state}.currentIdx`),
    0,
    "native range Home reaches first commit",
  );
  await key(page, "ArrowRight");
  assert.equal(
    await evaluate(page, `${state}.currentIdx`),
    1,
    "native range keys reach an intermediate commit",
  );
  assert.deepEqual(
    await evaluate(
      page,
      `${app}.forceGraphModule.getGraph().graphData().nodes.map(n=>n.id).sort()`,
    ),
    ["browser-other", "browser-root"],
    "scrubbing applies recorded closure",
  );
  await evaluate(page, `document.querySelector('#tt-speed').focus()`);
  await key(page, "End");
  assert.equal(await evaluate(page, `${state}.speed`), 10, "native selector chooses fastest speed");
  await key(page, "ArrowUp");
  const selectedSpeed = await evaluate(page, `${state}.speed`);
  await evaluate(page, `${app}.initForceGraphView()`);
  const refreshed = await evaluate(
    page,
    `({speed:${state}.speed,control:document.querySelector('#tt-speed').value})`,
  );
  await evaluate(
    page,
    `(async () => {
    const h=await (await fetch('./data/history.json')).json();
    ${app}.forceGraphModule.initTimeTravel({...h,commits:h.commits.slice(0,1)});
    ${app}.forceGraphModule.startTimeTravel();
  })()`,
  );
  const single = await evaluate(
    page,
    `({index:${state}.currentIdx,slider:document.querySelector('#tt-slider').value,
    position:document.querySelector('#tt-position').textContent})`,
  );
  records.push({ timelineControls: { selectedSpeed, refreshed, single }, page: page.name });
  assert.deepEqual(
    { selectedSpeed, refreshed, single },
    {
      selectedSpeed: 5,
      refreshed: { speed: 5, control: "5" },
      single: { index: 0, slider: "0", position: "1 / 1" },
    },
    "keyboard speed, refreshed preference and single-commit boundary agree",
  );
  await evaluate(page, `${app}.initForceGraphView()`);
  await key(page, "t", "KeyT");
  await click(page, "#tt-play");
  assert.equal(await evaluate(page, `${state}.playing`), true);
  await waitFor(
    page,
    `${state}.currentIdx === 3 && !${state}.playing`,
    "5x playback reaches end and stops",
    2000,
  );
  assert.deepEqual(
    await evaluate(
      page,
      `${app}.forceGraphModule.getGraph().graphData().nodes.map(n=>n.id).sort()`,
    ),
    ["browser-detail", "browser-other", "browser-root"],
  );
  await capture(page, "timeline-controls");
  clean(page);
  console.log(
    `PASS: ${page.name} native scrubber, keyboard speed, retained preference, single commit and playback completion`,
  );
}

async function timelineJourney(page) {
  await ready(page);
  await waitFor(
    page,
    "!!navigator.serviceWorker.controller",
    "timeline fixture worker controls page",
  );
  await delay(500);
  await ready(page);
  await click(page, 'a[href="#/graph"]');
  await waitFor(
    page,
    `${app}.forceGraphReady && !${app}.forceGraphLoading`,
    "timeline graph loaded",
  );
  const state = `${app}.forceGraphModule.getTimeTravelState()`;
  assert.equal(
    await evaluate(page, `${state}.totalCommits`),
    4,
    "actual exported lifecycle history loaded",
  );
  await key(page, "t", "KeyT");
  await waitFor(page, `${state}.active`, "keyboard enters timeline");
  const expected = [
    ["browser-detail", "browser-other", "browser-root"],
    ["browser-other", "browser-root"],
    ["browser-other", "browser-root"],
    ["browser-detail", "browser-other", "browser-root"],
  ];
  for (let idx = 0; idx < expected.length; idx++) {
    if (idx) await click(page, "#tt-forward");
    assert.equal(await evaluate(page, `${state}.currentIdx`), idx);
    assert.deepEqual(
      await evaluate(
        page,
        `${app}.forceGraphModule.getGraph().graphData().nodes.map(n=>n.id).sort()`,
      ),
      expected[idx],
      "create, close, closed edit and reopen visibility follows recorded states",
    );
    records.push({ timeline: idx, page: page.name, visible: expected[idx] });
  }
  await click(page, "#tt-back");
  assert.deepEqual(
    await evaluate(
      page,
      `${app}.forceGraphModule.getGraph().graphData().nodes.map(n=>n.id).sort()`,
    ),
    expected[2],
  );
  await click(page, ".timeline-close");
  assert.equal(await evaluate(page, `${state}.active`), false);
  assert.deepEqual(
    await evaluate(
      page,
      `${app}.forceGraphModule.getGraph().graphData().nodes.map(n=>n.id).sort()`,
    ),
    ["browser-closed", "browser-detail", "browser-other", "browser-root"],
    "exiting restores current graph",
  );
  await key(page, "t", "KeyT");
  await click(page, "#tt-play");
  await waitFor(page, `${state}.currentIdx > 0`, "playback advances real history");
  await click(page, "#tt-play");
  const paused = await evaluate(page, state);
  assert.equal(paused.playing, false);
  await delay(1200);
  assert.equal(
    await evaluate(page, `${state}.currentIdx`),
    paused.currentIdx,
    "pause stops advancement",
  );
  await click(page, "#tt-start");
  await click(page, "#tt-play");
  assert.equal(
    await evaluate(page, `${state}.playing`),
    true,
    "playback active before data replacement",
  );
  await evaluate(
    page,
    `(() => {
    window.__timelineTicks=0;
    document.addEventListener('bv-graph:timeTravelCommit',()=>window.__timelineTicks++);
    const d=getGraphViewData();
    ${app}.forceGraphModule.loadData(d.issues.filter(i=>i.id==='browser-other'),[],null);
  })()`,
  );
  await delay(1200);
  const replaced = await evaluate(
    page,
    `({state:${state},ticks:window.__timelineTicks,
    nodes:${app}.forceGraphModule.getGraph().graphData().nodes.map(n=>n.id),
    controls:!!document.querySelector('#time-travel-controls')})`,
  );
  assert.deepEqual(
    replaced,
    {
      state: { active: false, playing: false, currentIdx: 0, totalCommits: 0, speed: 1 },
      ticks: 0,
      nodes: ["browser-other"],
      controls: false,
    },
    "old playback cannot overwrite replacement data",
  );
  await evaluate(page, `${app}.initForceGraphView()`);
  assert.equal(
    await evaluate(page, `${state}.totalCommits`),
    4,
    "fresh export history can be loaded again",
  );
  await key(page, "t", "KeyT");
  await click(page, "#tt-play");
  assert.equal(
    await evaluate(page, `${state}.playing`),
    true,
    "playback active before history replacement",
  );
  await evaluate(
    page,
    `(() => {${app}.forceGraphModule.initTimeTravel(null);window.__timelineTicks=0;})()`,
  );
  await delay(1200);
  const cleared = await evaluate(
    page,
    `({state:${state},ticks:window.__timelineTicks,
    nodes:${app}.forceGraphModule.getGraph().graphData().nodes.map(n=>n.id).sort(),
    controls:!!document.querySelector('#time-travel-controls')})`,
  );
  assert.deepEqual(
    cleared,
    {
      state: { active: false, playing: false, currentIdx: 0, totalCommits: 0, speed: 1 },
      ticks: 0,
      nodes: ["browser-closed", "browser-detail", "browser-other", "browser-root"],
      controls: false,
    },
    "missing replacement history stops playback and restores the current graph",
  );
  await evaluate(page, `${app}.initForceGraphView()`);
  await key(page, "t", "KeyT");
  await click(page, "#tt-play");
  assert.equal(await evaluate(page, `${state}.playing`), true, "playback active before cleanup");
  await evaluate(page, `(() => {${app}.forceGraphModule.cleanup();window.__timelineTicks=0;})()`);
  await delay(1200);
  const cleaned = await evaluate(
    page,
    `({state:${state},ticks:window.__timelineTicks,
    graph:${app}.forceGraphModule.getGraph(),controls:!!document.querySelector('#time-travel-controls')})`,
  );
  assert.deepEqual(
    cleaned,
    {
      state: { active: false, playing: false, currentIdx: 0, totalCommits: 0, speed: 1 },
      ticks: 0,
      graph: null,
      controls: false,
    },
    "cleanup cancels active playback and releases history",
  );
  records.push({
    timelineReplacement: replaced,
    timelineHistoryCleared: cleared,
    timelineCleanup: cleaned,
    page: page.name,
  });
  await capture(page, "timeline");
  clean(page);
  console.log(
    `PASS: ${page.name} exported timeline navigation, play/pause, replacement recovery and active cleanup`,
  );
}

async function historyLoadingJourney(page) {
  await ready(page);
  await waitFor(
    page,
    "!!navigator.serviceWorker.controller",
    "history fixture worker controls page",
  );
  await delay(500);
  await ready(page);
  await send("Network.setBypassServiceWorker", { bypass: true }, page.session);
  await click(page, 'a[href="#/graph"]');
  await waitFor(
    page,
    `${app}.forceGraphReady && !${app}.forceGraphLoading`,
    "initial graph loaded",
  );
  await evaluate(
    page,
    `(() => {window.__historyGraphLoads=0;document.addEventListener('bv-graph:dataLoaded',()=>window.__historyGraphLoads++);})()`,
  );
  let loads = 0;
  for (const variant of [
    "stalled-headers",
    "empty",
    "stalled-body",
    "empty",
    "malformed",
    "missing",
    "empty",
  ]) {
    historyVariant = variant;
    const requestsBefore = records.filter((r) => r.historyRequest).length;
    const start = Date.now();
    // Start without awaiting: an unbounded request must fail our browser assertion,
    // rather than hang the CDP evaluation waiting for the app's promise.
    await evaluate(page, `void ${app}.initForceGraphView()`);
    await waitFor(
      page,
      `${app}.forceGraphReady && !${app}.forceGraphLoading && !${app}.forceGraphError`,
      `${variant}: optional history must release graph loading`,
      6000,
    );
    loads++;
    const requests = records.filter((r) => r.historyRequest).slice(requestsBefore);
    assert.equal(requests.length, 1, "refresh reaches the optional history endpoint");
    assert.equal(requests[0].variant, variant);
    assert.equal(
      await evaluate(page, "window.__historyGraphLoads"),
      loads,
      "every refresh must actually run",
    );
    assert.deepEqual(
      await evaluate(
        page,
        `${app}.forceGraphModule.getGraph().graphData().nodes.map(n=>n.id).sort()`,
      ),
      ["browser-closed", "browser-detail", "browser-other", "browser-root"],
    );
    records.push({
      historyLoading: variant,
      page: page.name,
      elapsedMs: Date.now() - start,
      loads,
    });
  }
  historyVariant = null;
  await capture(page, "history-loading");
  clean(page);
  console.log(
    `PASS: ${page.name} optional history stalls, malformed/missing fallback and refresh recovery`,
  );
}

async function graphReloadJourney(page) {
  await ready(page);
  await waitFor(
    page,
    "!!navigator.serviceWorker.controller",
    "reload fixture worker controls page",
  );
  await delay(500);
  await ready(page);
  await click(page, 'a[href="#/graph"]');
  await waitFor(
    page,
    `${app}.forceGraphReady && !${app}.forceGraphLoading`,
    "initial graph loaded",
  );
  const metrics = `(() => {const m=${app}.forceGraphModule.getMetrics();return {
    vectors:{...Object.fromEntries(['pagerank','betweenness','criticalPath','eigenvector','kcore','slack'].map(k=>[k,m[k] === null ? null : Array.from(m[k])])),
      hitsHub:m.hits === null ? null : Array.from(m.hits.hub),hitsAuthority:m.hits === null ? null : Array.from(m.hits.authority)},
    cycles:m.cycles.cycles,articulation:[...m.articulationPoints],
    nodes:${app}.forceGraphModule.getGraph().graphData().nodes.map(n=>({id:n.id,pagerank:n.pagerank,betweenness:n.betweenness}))};})()`;
  const before = await evaluate(page, metrics);
  assert.equal(before.vectors.betweenness.length, 4, "initial real WASM metric covers four nodes");
  for (let cycle = 0; cycle < 2; cycle++) {
    await evaluate(page, `${app}.forceGraphModule.loadData([],[],null)`);
    const empty = await evaluate(page, metrics);
    assert.equal(
      empty.vectors.betweenness,
      null,
      "skipped empty-graph metric must not retain old scores",
    );
    for (const [name, values] of Object.entries(empty.vectors)) {
      assert.ok(
        values === null || values.length === 0,
        `${name}: no scores from the previous graph`,
      );
    }
    assert.deepEqual(empty.cycles, []);
    assert.deepEqual(empty.articulation, []);
    assert.deepEqual(empty.nodes, []);
    assert.equal(await evaluate(page, `${app}.forceGraphModule.getWasmGraph().nodeCount()`), 0);
    await evaluate(
      page,
      `(() => {const d=getGraphViewData();${app}.forceGraphModule.loadData(d.issues,d.dependencies,null);})()`,
    );
    const restored = await evaluate(page, metrics);
    assert.deepEqual(restored, before, "fresh computation restores exact metrics and node scores");
    records.push({ graphReload: cycle, page: page.name, empty, restored });
  }
  const navigation = await evaluate(
    page,
    `(() => {
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
  })()`,
  );
  assert.equal(navigation.selected.active, true);
  assert.equal(navigation.selected.cycleCount, 1);
  assert.deepEqual([...navigation.selected.currentCycle].sort(), [
    "browser-detail",
    "browser-root",
  ]);
  assert.ok(navigation.path?.path.length >= 2, "real critical path animation started");
  await delay(1000); // Includes both traversal ticks and the delayed completion callback.
  const afterPath = await evaluate(
    page,
    `({state:${app}.forceGraphModule.getCriticalPathState(),events:window.__oldPathEvents})`,
  );
  records.push({ navigationReload: navigation, afterPath, page: page.name });
  assert.deepEqual(
    { cycle: navigation.afterReload, path: afterPath },
    {
      cycle: { active: false, cycleCount: 0, currentIndex: 0, currentCycle: [], currentPath: "" },
      path: { state: { active: false, path: [], length: 0, currentStep: 0 }, events: [] },
    },
    "graph replacement drops old cycle IDs and cancels old critical-path callbacks",
  );
  const cleanup = await evaluate(
    page,
    `(() => {
    const m=${app}.forceGraphModule,d=getGraphViewData();
    m.loadData(d.issues,[...d.dependencies,{issue_id:'browser-root',depends_on_id:'browser-other',type:'blocks'},
      {issue_id:'browser-other',depends_on_id:'browser-root',type:'blocks'}],null);
    m.initCycleNavigator();m.highlightCycle(0,false);
    const selected=m.getCycleNavigatorState();
    const next=m.nextCycle(),prev=m.prevCycle();
    m.cleanup();
    return {selected,next,prev,after:m.getCycleNavigatorState(),graph:m.getGraph(),path:m.getCriticalPathState()};
  })()`,
  );
  for (const cycle of [cleanup.selected.currentCycle, cleanup.next.cycle, cleanup.prev.cycle]) {
    assert.deepEqual(
      [...cycle].sort(),
      ["browser-other", "browser-root"],
      "new navigation uses only the replacement cycle",
    );
  }
  assert.deepEqual(cleanup.after, {
    active: false,
    cycleCount: 0,
    currentIndex: 0,
    currentCycle: [],
    currentPath: "",
  });
  assert.equal(cleanup.graph, null);
  assert.equal(cleanup.path.active, false);
  records.push({ navigationCleanup: cleanup, page: page.name });
  const cleanupPath = await evaluate(
    page,
    `(async () => {
    const m=${app}.forceGraphModule,d=getGraphViewData();
    await m.initGraph('graph-container');
    m.loadData(d.issues,d.dependencies,null);
    const path=m.animateCriticalPath(true);
    m.cleanup();
    window.__oldPathEvents=[];
    return path;
  })()`,
  );
  assert.ok(cleanupPath?.path.length >= 2, "real animation started immediately before cleanup");
  await delay(1000);
  const afterCleanup = await evaluate(
    page,
    `({state:${app}.forceGraphModule.getCriticalPathState(),events:window.__oldPathEvents,graph:${app}.forceGraphModule.getGraph()})`,
  );
  assert.deepEqual(
    afterCleanup,
    { state: { active: false, path: [], length: 0, currentStep: 0 }, events: [], graph: null },
    "cleanup cancels active traversal and its delayed completion",
  );
  records.push({ activePathCleanup: afterCleanup, page: page.name });
  await capture(page, "graph-reload");
  clean(page);
  console.log(
    `PASS: ${page.name} fresh metrics, replacement cycle navigation, cancelled path animation and cleanup`,
  );
}

async function graphStartupJourney(page) {
  const readyLarge = `typeof Alpine !== 'undefined' && !${app}.loading && ${app}.stats.total===1000 && ${app}.graphReady`;
  await waitFor(page, readyLarge, "1000-node startup fixture ready");
  await waitFor(page, "!!navigator.serviceWorker.controller", "startup worker controls page");
  await delay(500);
  await waitFor(page, readyLarge, "startup fixture after worker activation");
  await send("Network.setBypassServiceWorker", { bypass: true }, page.session);
  await click(page, 'a[href="#/graph"]');
  await waitFor(
    page,
    `${app}.forceGraphReady && !${app}.forceGraphLoading`,
    "initial graph loaded",
  );
  await evaluate(
    page,
    `(() => {
    const g=${app}.forceGraphModule.getGraph(),draw=g.nodeCanvasObject();
    g.nodeCanvasObject(function(node,...args){
      const p=window.__startupSample;
      if(p?.loaded && !p.paint && window.__startupNodes.has(node)
        && Number.isFinite(node.x) && Number.isFinite(node.y))p.paint=performance.now();
      return draw.call(this,node,...args);
    });
  })()`,
  );
  const samples = { valid: [], missing: [] };
  for (let round = 0; round < 5; round++) {
    for (const variant of round % 2 ? ["missing", "valid"] : ["valid", "missing"]) {
      layoutVariant = variant;
      await evaluate(
        page,
        `(() => {
        window.__startupSample={started:performance.now()};
        document.addEventListener('bv-graph:dataLoaded',e=>{
          const p=window.__startupSample,g=${app}.forceGraphModule.getGraph();
          p.loaded=performance.now();p.precomputed=e.detail.precomputed;
          p.nodes=g.graphData().nodes.length;p.links=g.graphData().links.length;
          p.warmup=g.warmupTicks();
          window.__startupNodes=new WeakSet(g.graphData().nodes);
        },{once:true});
        ${app}.initForceGraphView();
      })()`,
      );
      await waitFor(page, "!!window.__startupSample.paint", "actual first canvas paint");
      const result = await evaluate(page, "window.__startupSample");
      assert.deepEqual(
        [result.nodes, result.links, result.precomputed],
        [1000, 900, variant === "valid"],
        "same full graph rendered for both startup paths",
      );
      samples[variant].push(result.paint - result.started);
      records.push({ graphStartup: { round, variant, result }, page: page.name });
      await waitFor(page, `!${app}.forceGraphLoading`, "startup view ready");
    }
  }
  const median = (a) => [...a].sort((a, b) => a - b)[2];
  const summary = {
    samples,
    seededMedian: median(samples.valid),
    fallbackMedian: median(samples.missing),
  };
  records.push({ graphStartupSummary: summary, page: page.name });
  console.log(`Graph startup ${page.name}: ${JSON.stringify(summary)}`);
  assert.ok(
    summary.seededMedian < summary.fallbackMedian * 0.8,
    "validated seeds reduce median force-view first-paint latency by at least20%",
  );
  layoutVariant = null;
  clean(page);
  console.log(`PASS: ${page.name} measured precomputed startup benefit with full graph parity`);
}

async function precomputedMetricsJourney(page) {
  await ready(page);
  await waitFor(page, "!!navigator.serviceWorker.controller", "centrality worker controls page");
  await delay(500);
  await ready(page);
  await send("Network.setBypassServiceWorker", { bypass: true }, page.session);
  const oracle = await evaluate(
    page,
    `(() => {
    const g=GRAPH_STATE.graph, pr=g.pagerankDefault(), bt=g.betweenness();
    return Object.fromEntries([...GRAPH_STATE.nodeMap].map(([id,i])=>[id,{pr:pr[i],bt:bt[i]}]));
  })()`,
  );
  await evaluate(
    page,
    `(() => {
    const p=window.bvGraphWasm.DiGraph.prototype;
    window.__centralityCalls={};
    for(const name of ['pagerankDefault','betweenness','betweennessApprox']) {
      const original=p[name];
      p[name]=function(...args){window.__centralityCalls[name]=(window.__centralityCalls[name]||0)+1;return original.apply(this,args);};
    }
  })()`,
  );
  const variants = [
    "valid",
    "metric-missing",
    "metric-invalid",
    "metric-timeout",
    "metric-approximate",
    "metric-old-version",
    "stale-edge",
    "missing",
    "valid",
  ];
  for (const variant of variants) {
    layoutVariant = variant;
    await evaluate(
      page,
      `(() => {
      window.__centralityCalls={};window.__centralityLoad=null;
      document.addEventListener('bv-graph:dataLoaded',e=>{
        window.__centralityLoad={reused:e.detail.precomputedMetrics,calls:{...window.__centralityCalls},
          nodes:${app}.forceGraphModule.getGraph().graphData().nodes.map(n=>({id:n.id,pr:n.pagerank,bt:n.betweenness}))};
      },{once:true});
    })()`,
    );
    if (await evaluate(page, `${app}.view !== 'graph'`)) await click(page, 'a[href="#/graph"]');
    else await evaluate(page, `${app}.initForceGraphView()`);
    await waitFor(page, "!!window.__centralityLoad", `${variant} centrality loaded`);
    const result = await evaluate(page, "window.__centralityLoad");
    const expected =
      variant === "valid"
        ? ["pagerank", "betweenness"]
        : ["metric-missing", "metric-timeout"].includes(variant)
          ? ["betweenness"]
          : ["metric-invalid", "metric-approximate"].includes(variant)
            ? ["pagerank"]
            : [];
    assert.deepEqual(
      result.reused,
      expected,
      `${variant}: only complete computed centrality is reused`,
    );
    assert.equal(result.calls.pagerankDefault || 0, expected.includes("pagerank") ? 0 : 1);
    assert.equal(result.calls.betweenness || 0, expected.includes("betweenness") ? 0 : 1);
    for (const n of result.nodes) {
      assert.ok(
        Math.abs(n.pr - oracle[n.id].pr) < 0.00001,
        `${variant}: ${n.id} PageRank agrees within algorithm convergence tolerance`,
      );
      assert.equal(n.bt, oracle[n.id].bt, `${variant}: exact directed betweenness agrees`);
    }
    records.push({ centrality: { variant, result }, page: page.name });
    await waitFor(page, `!${app}.forceGraphLoading`, "centrality view finishes");
  }
  clean(page);
  layoutVariant = null;
  console.log(
    `PASS: ${page.name} centrality reuse, actual avoided WASM calls, parity and fallback`,
  );
}

async function layoutSeedsJourney(page, edgeless = false) {
  await ready(page);
  await waitFor(page, "!!navigator.serviceWorker.controller", "layout worker controls page");
  await delay(500);
  await ready(page);
  // Exercise each optional artifact response against the same real SQLite/WASM
  // source. Bypass the worker cache so stale/corrupt origin responses reach fetch.
  await send("Network.setBypassServiceWorker", { bypass: true }, page.session);
  await evaluate(
    page,
    `(() => {window.__layoutHovered=null;document.addEventListener('bv-graph:nodeHover',e=>{window.__layoutHovered=e.detail?.node?.id || null;});})()`,
  );
  const exported = JSON.parse(
    fs.readFileSync(path.join(activeBundle, "data/graph_layout.json"), "utf8"),
  );
  const expected = exported.positions;
  if (edgeless) {
    assert.deepEqual(exported.links, [], "edgeless exporter emits an empty array");
    assert.equal(exported.edge_count, 0);
    assert.equal(
      await evaluate(page, "getGraphViewData().dependencies.length"),
      0,
      "actual SQLite graph is edgeless",
    );
    assert.equal(
      await evaluate(page, "GRAPH_STATE.graph.edgeCount()"),
      0,
      "actual WASM graph is edgeless",
    );
  }
  const variants = edgeless
    ? ["valid", "missing", "valid"]
    : [
        "valid",
        "missing",
        "malformed",
        "partial",
        "stale-edge",
        "stale-node",
        "invalid-coordinate",
        "stalled-headers",
        "stalled-body",
        "valid",
      ];
  for (const variant of variants) {
    layoutVariant = variant;
    await evaluate(
      page,
      `(() => {
      window.__layoutLoaded = null;
      document.addEventListener('bv-graph:dataLoaded', e => {
        const m=${app}.forceGraphModule;
        window.__layoutLoaded = {precomputed:e.detail.precomputed,
          nodes:m.getGraph().graphData().nodes.map(n=>({id:n.id,x:n.x,y:n.y,fx:n.fx,fy:n.fy,pagerank:n.pagerank})),
          metrics:Object.fromEntries(Object.entries(m.getMetrics()).map(([k,v])=>[k,v !== null && v !== undefined]))};
      }, {once:true});
    })()`,
    );
    if (await evaluate(page, `${app}.view !== 'graph'`)) await click(page, 'a[href="#/graph"]');
    else await evaluate(page, `${app}.initForceGraphView()`);
    await waitFor(page, "!!window.__layoutLoaded", `${variant}: actual viewer graph load`);
    const initial = await evaluate(page, "window.__layoutLoaded");
    assert.equal(
      initial.precomputed,
      variant === "valid",
      `${variant}: accepted only matching layout`,
    );
    assert.equal(initial.nodes.length, 4);
    for (const n of initial.nodes) {
      assert.equal(n.fx, null, `${variant}: ${n.id} free on x`);
      assert.equal(n.fy, null, `${variant}: ${n.id} free on y`);
      assert.ok(
        Number.isFinite(n.pagerank) && n.pagerank !== 999,
        "actual browser metric, not exported sentinel",
      );
      if (variant === "valid")
        assert.deepEqual([n.x, n.y], expected[n.id], `${n.id}: exact exported seed at dataLoaded`);
    }
    for (const metric of [
      "pagerank",
      "betweenness",
      "criticalPath",
      "eigenvector",
      "kcore",
      "cycles",
    ]) {
      assert.equal(initial.metrics[metric], true, `${metric} remains computed`);
    }
    records.push({ layoutVariant: variant, initial });
    await waitFor(page, `!${app}.forceGraphLoading`, "viewer finishes graph initialization");
    await waitFor(
      page,
      `${app}.graphLoadingStage === null`,
      "simulation loading overlay completes",
    );
    await delay(400); // Let Alpine's 300ms leave transition finish before the next reload.
  }
  await delay(1000);
  assert.ok(
    await evaluate(
      page,
      `${app}.forceGraphModule.getGraph().graphData().nodes.some(n=>{const p=${JSON.stringify(expected)}[n.id];return n.x!==p[0] || n.y!==p[1];})`,
    ),
    "live simulation moves seeded nodes",
  );
  await evaluate(page, `${app}.forceGraphModule.setFilter('search','Orchid root')`);
  assert.deepEqual(
    await evaluate(page, `${app}.forceGraphModule.getGraph().graphData().nodes.map(n=>n.id)`),
    ["browser-root"],
  );
  await evaluate(page, `${app}.forceGraphModule.setFilter('search','')`);
  await delay(1000);
  // Movement was verified above. End physics before targeting a real canvas
  // node so an isolated component cannot move between measurement and click.
  await evaluate(page, `${app}.forceGraphModule.getGraph().cooldownTicks(0)`);
  await delay(100);
  await evaluate(page, `${app}.forceGraphModule.getGraph().zoomToFit(0,50)`);
  await delay(100);
  const point = await evaluate(
    page,
    `(() => {const g=${app}.forceGraphModule.getGraph();const n=g.graphData().nodes.find(n=>n.id==='browser-root');const p=g.graph2ScreenCoords(n.x,n.y);const r=document.querySelector('#graph-container canvas').getBoundingClientRect();return {x:r.x+p.x,y:r.y+p.y};})()`,
  );
  await evaluate(
    page,
    `(() => {window.__graphPointerEvents=[];for(const type of ['pointerdown','pointerup','click','bv-graph:nodeClick','bv-graph:backgroundClick']) document.addEventListener(type,e=>window.__graphPointerEvents.push({type,target:e.target.tagName,id:e.detail?.node?.id,x:e.clientX,y:e.clientY,buttons:e.buttons}),{capture:true});})()`,
  );
  await send("Input.dispatchMouseEvent", { type: "mouseMoved", ...point }, page.session);
  // ForceGraph throttles its hit-test canvas; visible coordinates alone do
  // not prove the pointer target has caught up with the last zoom.
  await waitFor(
    page,
    `window.__layoutHovered === 'browser-root'`,
    "actual graph hit-test recognizes the target node",
  );
  await send(
    "Input.dispatchMouseEvent",
    { type: "mousePressed", ...point, button: "left", buttons: 1, clickCount: 1 },
    page.session,
  );
  await delay(50);
  await send(
    "Input.dispatchMouseEvent",
    { type: "mouseReleased", ...point, button: "left", buttons: 0, clickCount: 1 },
    page.session,
  );
  await delay(100);
  records.push({
    graphPointer: await evaluate(page, "window.__graphPointerEvents"),
    point,
    page: page.name,
  });
  await waitFor(
    page,
    `${app}.graphDetailNode?.id === 'browser-root'`,
    "seeded graph pointer opens detail after filter",
  );
  const fitsContainer = `(() => {const g=${app}.forceGraphModule.getGraph();const c=document.getElementById('graph-container');return g.width()===c.clientWidth && g.height()===c.clientHeight;})()`;
  await delay(400);
  await waitFor(page, fitsContainer, "graph fits container with detail open");
  await capture(page, "layout-seeds");
  await click(page, 'button[title="Close detail pane (Esc)"]');
  await waitFor(page, `!${app}.graphDetailNode`, "close detail pane");
  await delay(300);
  await waitFor(page, fitsContainer, "graph fits container after detail closes");
  clean(page);
  assert.ok(
    records.some((r) => r.serverRequest === "/data/graph_layout.json"),
    "actual layout HTTP request",
  );
  layoutVariant = null;
  console.log(
    `PASS: ${page.name} matching seeds, ${variants.length} layout cases, metrics, movement, filter and detail`,
  );
}
async function blockingTypesJourney(page) {
  await waitFor(
    page,
    `typeof Alpine !== 'undefined' && !${app}.loading && ${app}.stats.total === 7 && ${app}.graphReady`,
    "seven workflow issues and actual graph WASM",
  );
  await waitFor(
    page,
    "!!navigator.serviceWorker.controller",
    "workflow bundle worker controls page",
  );
  await delay(500);
  await waitFor(
    page,
    `typeof Alpine !== 'undefined' && !${app}.loading && ${app}.graphReady`,
    "workflow app after worker activation",
  );
  const active = ["workflow-blocks", "workflow-conditional", "workflow-legacy", "workflow-waits"];
  const all = [...active, "workflow-closed"].sort();
  const overview = await evaluate(
    page,
    `execQuery("SELECT dependent_count, blocks_ids, status FROM issue_overview_mv WHERE id = 'workflow-root'")[0]`,
  );
  assert.equal(overview.status, "qa-review", "custom workflow status is retained");
  assert.equal(overview.dependent_count, 4);
  assert.deepEqual(
    overview.blocks_ids.split(",").sort(),
    active,
    "active ID list matches exported count",
  );
  assert.deepEqual(
    await evaluate(page, "({blocked: getStats().blocked, actionable: getStats().actionable})"),
    { blocked: 4, actionable: 1 },
    "typed prerequisites keep dependents out of ready work",
  );
  assert.deepEqual(
    await evaluate(page, "getQuickWins().map(i=>i.id)"),
    ["workflow-related"],
    "informational reference is the only ready issue",
  );
  const closed = await evaluate(
    page,
    `execQuery("SELECT blocker_count, blocked_by_ids FROM issue_overview_mv WHERE id = 'workflow-closed'")[0]`,
  );
  assert.equal(closed.blocker_count, 0);
  assert.ok(!closed.blocked_by_ids, "closed endpoint has no active blockers");
  for (const id of active) {
    const row = await evaluate(
      page,
      `execQuery("SELECT blocker_count, blocked_by_ids FROM issue_overview_mv WHERE id = ?", [${JSON.stringify(id)}])[0]`,
    );
    assert.equal(row.blocker_count, 1);
    assert.equal(row.blocked_by_ids, "workflow-root");
    assert.deepEqual(
      await evaluate(page, `getIssueDependencies(${JSON.stringify(id)}).blockedBy.map(i=>i.id)`),
      ["workflow-root"],
      `${id}: correctly directed blocker lookup`,
    );
  }
  assert.deepEqual(
    await evaluate(page, `getIssueDependencies('workflow-root').blocks.map(i=>i.id).sort()`),
    all,
    "detail navigation retains historical relationships",
  );
  assert.deepEqual(
    await evaluate(page, `getIssueDependencies('workflow-related')`),
    { blocks: [], blockedBy: [] },
    "custom informational relation remains nonblocking",
  );
  const expected = [
    ["workflow-blocks", "blocks"],
    ["workflow-closed", "waits-for"],
    ["workflow-conditional", "conditional-blocks"],
    ["workflow-legacy", ""],
    ["workflow-waits", "waits-for"],
  ];
  assert.deepEqual(
    await evaluate(
      page,
      `getGraphViewData().dependencies.map(d=>[d.issue_id,d.type]).sort((a,b)=>a[0].localeCompare(b[0]))`,
    ),
    expected,
    "graph query retains all blocking variants and original types",
  );
  assert.deepEqual(
    await evaluate(page, "[GRAPH_STATE.graph.nodeCount(), GRAPH_STATE.graph.edgeCount()]"),
    [7, 5],
  );
  await send("Page.navigate", { url: origin + "/#/issue/workflow-conditional" }, page.session);
  await waitFor(
    page,
    `typeof Alpine !== 'undefined' && ${app}.selectedIssue?.id === 'workflow-conditional'`,
    "conditional dependent details",
  );
  await key(page, "h", "KeyH");
  await waitFor(
    page,
    `${app}.selectedIssue?.id === 'workflow-root'`,
    "h navigates to prerequisite",
  );
  await key(page, "l", "KeyL");
  await waitFor(
    page,
    `${app}.selectedIssue?.id === 'workflow-blocks'`,
    "l navigates to first dependent",
  );
  await capture(page, "dependency-navigation");
  await key(page, "Escape");
  await click(page, 'a[href="#/graph"]');
  await waitFor(
    page,
    `${app}.forceGraphReady && !${app}.forceGraphLoading && !!document.querySelector('#graph-container canvas')`,
    "force graph draws blocking variants",
  );
  assert.equal(
    await evaluate(page, `${app}.forceGraphModule.getWasmGraph().edgeCount()`),
    5,
    "force graph WASM uses same edges",
  );
  assert.deepEqual(
    await evaluate(
      page,
      `${app}.forceGraphModule.getGraph().graphData().links.map(e=>[e.source.id,e.target.id,e.type]).sort((a,b)=>a[0].localeCompare(b[0]))`,
    ),
    expected.map(([id, type]) => [id, "workflow-root", type || "blocks"]),
    "rendered links preserve endpoint direction",
  );
  await capture(page, "blocking-graph");
  clean(page);
  console.log(
    "PASS: real exported SQLite, blocking ID lists, issue h/l navigation, graph WASM and force-graph links",
  );
}

// Export the readiness fixture with closed rows excluded: three ready tasks
// (open, in-progress, resolved prerequisites), a missing dependency, a deferred
// task, a child inheriting its parent's review gate, that parent, a qa-review
// prerequisite and an explicit blocked lifecycle. Closed/deleted prerequisites
// remain in the full source; missing is genuinely absent.
async function readinessJourney(page) {
  await waitFor(
    page,
    `typeof Alpine !== 'undefined' && !${app}.loading && ${app}.stats.total === 9 && ${app}.graphReady`,
    "nine visible readiness issues and actual graph WASM",
  );
  await waitFor(page, "!!navigator.serviceWorker.controller", "readiness worker controls page");
  await delay(500);
  await waitFor(
    page,
    `typeof Alpine !== 'undefined' && !${app}.loading && ${app}.graphReady`,
    "readiness app after worker activation",
  );
  const readyIDs = ["ready-open", "ready-progress", "ready-resolved"];
  const blockedIDs = ["missing-child", "parent", "parent-child"];
  const stats = await evaluate(page, "getStats()");
  console.log(`${page.name} readiness:`, JSON.stringify(stats));
  assert.equal(
    stats.actionable,
    3,
    "only three proven ready tasks, including in-progress and omitted resolved prerequisites",
  );
  assert.equal(stats.blocked, 3, "missing and inherited gates remain blocked");
  assert.equal(stats.active, 9, "active count includes unresolved lifecycles exactly once");
  assert.equal(stats.closed, 0, "absent lifecycle count is zero");
  assert.deepEqual(
    await evaluate(page, "getQuickWins(20).map(i=>i.id).sort()"),
    readyIDs,
    "quick wins exclude deferred, unknown, inherited and nonready lifecycle tasks",
  );
  assert.deepEqual(
    await evaluate(page, "getActionableIssues().sort()"),
    readyIDs,
    "actionable lookup uses full-source readiness rather than synthetic graph roots",
  );
  assert.ok(
    (await evaluate(page, "topWhatIf(100).map(i=>i.issueId)")).every((id) => readyIDs.includes(id)),
    "cascade recommendations consider only proven ready issues",
  );
  assert.deepEqual(
    await evaluate(page, "queryIssues({hasBlockers:false}).map(i=>i.id).sort()"),
    readyIDs,
    "Ready filter uses the same readiness policy",
  );
  const readyCard = '[\\@click*="filters.hasBlockers = false"][\\@click*="view ="]';
  await click(page, readyCard);
  await resultIDs(page, readyIDs);
  await capture(page, "ready-filter");
  await click(page, 'a[href="#/"]');
  await click(page, '[\\@click*="filters.hasBlockers = true"][\\@click*="view ="]');
  await resultIDs(page, blockedIDs);
  await capture(page, "blocked-filter");
  await click(page, 'a[href="#/insights"]');
  await waitFor(
    page,
    `[...document.querySelectorAll('[x-text*="stats.active"]')].filter(${visible}).some(e=>e.textContent.trim()==='9')`,
    "visible finite active-node count",
  );
  await capture(page, "active-count");
  clean(page);
  console.log(
    `PASS: ${page.name} full-source readiness, quick wins, card filters and active counts`,
  );
}

// Reuse the asymmetric sim-* fixture below. Its leading dependent (hub) and
// prerequisite (authority) differ, so swapping the score vectors also fails.
async function hitsJourney(page) {
  await waitFor(
    page,
    `typeof Alpine !== 'undefined' && !${app}.loading && ${app}.stats.total === 6 && ${app}.graphReady`,
    "six visible issues and actual HITS WASM",
  );
  await waitFor(page, "!!navigator.serviceWorker.controller", "HITS bundle worker controls page");
  await delay(500);
  await waitFor(
    page,
    `typeof Alpine !== 'undefined' && !${app}.loading && ${app}.graphReady`,
    "HITS app after worker activation",
  );
  await click(page, 'a[href="#/insights"]');
  const raw = await evaluate(page, "GRAPH_STATE.graph.hitsDefault()");
  assert.ok(
    raw.hubs.some((v) => v > 0) && raw.authorities.some((v) => v > 0),
    "real engine computed both score vectors",
  );
  const before = await evaluate(
    page,
    "[GRAPH_STATE.graph.nodeCount(), GRAPH_STATE.graph.edgeCount()]",
  );
  for (const [state, field, title, leader] of [
    ["topByHITSHub", "hits_hub", "HITS Hubs", "sim-child"],
    ["topByHITSAuth", "hits_auth", "HITS Authorities", "sim-root"],
  ]) {
    await click(page, 'a[href="#/insights"]');
    const rows = await evaluate(
      page,
      `JSON.parse(JSON.stringify(${app}.${state}.map(i=>({id:i.id,title:i.title,score:i.${field}}))))`,
    );
    console.log(`${page.name} ${title}:`, JSON.stringify(rows));
    assert.equal(rows[0]?.id, leader, `${title}: correct directed leader appears in dashboard`);
    assert.equal(
      rows.length,
      6,
      `${title}: all visible issues are ranked, excluding synthetic tombstone`,
    );
    assert.ok(rows[0].score > 0, `${title}: positive leader score`);
    assert.ok(
      rows.every(
        (r, i) =>
          Number.isFinite(r.score) &&
          r.score >= 0 &&
          r.score <= 1 &&
          (i === 0 || rows[i - 1].score >= r.score),
      ),
      `${title}: finite normalized scores in descending order`,
    );
    const panel = `[...document.querySelectorAll('.metric-panel-premium')].find(e=>e.querySelector('h3')?.textContent.trim()===${JSON.stringify(title)})`;
    await waitFor(
      page,
      `${panel} && [...${panel}.querySelectorAll('.metric-item')].filter(${visible}).length === 5`,
      `${title}: visible rendered ranking cards`,
    );
    const rendered = await evaluate(
      page,
      `[...${panel}.querySelectorAll('.metric-item')].filter(${visible}).map(e=>({title:e.querySelector('.metric-item-title').textContent,score:e.querySelector('.metric-item-score').textContent}))`,
    );
    assert.deepEqual(
      rendered,
      rows.slice(0, 5).map((r) => ({ title: r.title, score: r.score.toFixed(3) })),
      `${title}: rendered scores and ordering match ranked issues`,
    );
    assert.equal(
      await evaluate(
        page,
        `[...${panel}.querySelectorAll('p')].some(e=>(${visible})(e) && e.textContent.includes('No HITS'))`,
      ),
      false,
      `${title}: empty-data fallback hidden`,
    );
    await click(page, `template[x-for*="${state}.slice"] + .metric-item`);
    await waitFor(
      page,
      `${app}.selectedIssue?.id === ${JSON.stringify(leader)}`,
      `${title}: card opens its actual issue`,
    );
    await capture(page, `${field}-detail`);
    await key(page, "Escape");
  }
  await click(page, 'a[href="#/insights"]');
  assert.deepEqual(
    await evaluate(page, "[GRAPH_STATE.graph.nodeCount(), GRAPH_STATE.graph.edgeCount()]"),
    before,
    "ranking and navigation do not mutate graph",
  );
  const slack = await evaluate(
    page,
    "Array.from(GRAPH_STATE.graph.slack()).map((value,idx)=>({id:GRAPH_STATE.graph.nodeId(idx),value})).filter(row=>getIssue(row.id))",
  );
  assert.ok(
    slack.some((row) => row.value > 0),
    "asymmetric fixture includes flexible work",
  );
  assert.deepEqual(
    await evaluate(page, "getIssuesBySlack(100,true).map(i=>i.id).sort()"),
    slack
      .filter((row) => row.value === 0)
      .map((row) => row.id)
      .sort(),
    "zero-slack filter excludes flexible work",
  );
  assert.deepEqual(
    await evaluate(page, "getIssuesBySlack(100,false).map(i=>i.slack)"),
    slack.map((row) => row.value).sort((a, b) => b - a),
    "flexible-work list preserves all actual slack scores",
  );
  await capture(page, "hits-panels");
  clean(page);
  console.log(
    `PASS: ${page.name} actual HITS hub/authority rankings, rendered scores and issue navigation`,
  );
}

// Six actual issues: rank-root and five rank-branch-N dependents. Every branch
// also depends on eleven missing-NN endpoints. Those endpoints remain in the
// graph, but must not crowd actual issues out of a limited ranking panel.
async function metricVisibilityJourney(page) {
  await waitFor(
    page,
    `typeof Alpine !== 'undefined' && !${app}.loading && ${app}.stats.total === 6 && ${app}.graphReady`,
    "six visible issues and actual graph WASM",
  );
  await waitFor(page, "!!navigator.serviceWorker.controller", "ranking worker controls page");
  await delay(500);
  await waitFor(
    page,
    `typeof Alpine !== 'undefined' && !${app}.loading && ${app}.graphReady`,
    "ranking app after worker activation",
  );
  await click(page, 'a[href="#/insights"]');
  const before = await evaluate(
    page,
    "[GRAPH_STATE.graph.nodeCount(), GRAPH_STATE.graph.edgeCount()]",
  );
  assert.deepEqual(before, [17, 60], "missing endpoints participate in real graph analysis");
  const visibleIDs = [
    "rank-branch-1",
    "rank-branch-2",
    "rank-branch-3",
    "rank-branch-4",
    "rank-branch-5",
    "rank-root",
  ];
  const initial = await evaluate(
    page,
    `JSON.parse(JSON.stringify(${app}.topByHITSAuth.map(i=>({id:i.id,score:i.hits_auth}))))`,
  );
  console.log(`${page.name} authority ranking:`, JSON.stringify(initial));
  assert.equal(
    initial.length,
    6,
    "authority ranking retains all six actual issues despite eleven missing endpoints",
  );
  assert.equal(initial[0].id, "rank-root", "real prerequisite leads authorities");
  assert.ok(initial[0].score > 0, "real prerequisite has positive authority");
  for (const [fn, field, vector, suffix] of [
    ["getTopByHITSAuth", "hits_auth", "GRAPH_STATE.graph.hitsDefault().authorities", ""],
    ["getTopByHITSHub", "hits_hub", "GRAPH_STATE.graph.hitsDefault().hubs", ""],
    ["getTopByKCore", "kcore", "GRAPH_STATE.graph.kcore()", ""],
    ["getTopByBetweenness", "betweenness", "GRAPH_STATE.graph.betweenness()", ""],
    ["getIssuesBySlack", "slack", "GRAPH_STATE.graph.slack()", ",true"],
    ["getIssuesBySlack", "slack", "GRAPH_STATE.graph.slack()", ",false"],
  ]) {
    const all = await evaluate(page, `${fn}(100${suffix}).map(i=>({id:i.id,score:i.${field}}))`);
    assert.deepEqual(
      all.map((i) => i.id).sort(),
      visibleIDs,
      `${fn}${suffix}: each actual issue appears once`,
    );
    const raw = await evaluate(
      page,
      `Array.from(${vector}).map((score,idx)=>({id:GRAPH_STATE.graph.nodeId(idx),score}))`,
    );
    for (const row of all) {
      assert.equal(
        row.score,
        raw.find((r) => r.id === row.id).score,
        `${fn}: preserves actual WASM score for ${row.id}`,
      );
      assert.ok(Number.isFinite(row.score), `${fn}: finite score`);
    }
    assert.ok(
      all.every((r, i) => i === 0 || all[i - 1].score >= r.score),
      `${fn}: descending order`,
    );
    for (const limit of [0, 1, 3, 10]) {
      assert.deepEqual(
        await evaluate(page, `${fn}(${limit}${suffix}).map(i=>i.id)`),
        all.slice(0, limit).map((i) => i.id),
        `${fn}${suffix}: limit counts actual issue rows`,
      );
    }
  }
  const panel = `[...document.querySelectorAll('.metric-panel-premium')].find(e=>e.querySelector('h3')?.textContent.trim()==='HITS Authorities')`;
  await waitFor(
    page,
    `${panel} && [...${panel}.querySelectorAll('.metric-item')].filter(${visible}).length === 5`,
    "five rendered authority cards",
  );
  const cards = await evaluate(
    page,
    `[...${panel}.querySelectorAll('.metric-item')].filter(${visible}).map(e=>e.querySelector('.metric-item-title').textContent)`,
  );
  assert.deepEqual(cards, [
    "Real prerequisite",
    "Visible branch 1",
    "Visible branch 2",
    "Visible branch 3",
    "Visible branch 4",
  ]);
  await capture(page, "authority-ranking");
  await click(page, 'template[x-for*="topByHITSAuth.slice"] + .metric-item');
  await waitFor(
    page,
    `${app}.selectedIssue?.id === 'rank-root'`,
    "authority card opens actual issue",
  );
  assert.deepEqual(
    await evaluate(page, "[GRAPH_STATE.graph.nodeCount(), GRAPH_STATE.graph.edgeCount()]"),
    before,
    "ranking does not remove missing graph context",
  );
  await capture(page, "authority-detail");
  clean(page);
  console.log(
    `PASS: ${page.name} visible ranking limits, raw scores, rendered cards and issue navigation`,
  );
}

// Six visible issues: root->child, three children of absent, and joint which
// requires both root and absent. Completing absent would fabricate root's gain.
async function suggestionVisibilityJourney(page) {
  await waitFor(
    page,
    `typeof Alpine !== 'undefined' && !${app}.loading && ${app}.stats.total === 6 && ${app}.graphReady`,
    "six suggestion issues and actual WASM",
  );
  await waitFor(page, "!!navigator.serviceWorker.controller", "suggestion worker controls page");
  await delay(500);
  await waitFor(
    page,
    `typeof Alpine !== 'undefined' && !${app}.loading && ${app}.graphReady`,
    "suggestion app after worker activation",
  );
  await click(page, 'a[href="#/insights"]');
  const before = await evaluate(
    page,
    "[GRAPH_STATE.graph.nodeCount(), GRAPH_STATE.graph.edgeCount()]",
  );
  assert.deepEqual(before, [7, 6], "missing prerequisite stays in the graph");
  const initial = await evaluate(page, `JSON.parse(JSON.stringify(${app}.topKSet))`);
  console.log(`${page.name} priority picks:`, JSON.stringify(initial));
  assert.deepEqual(
    initial.items.map((i) => i.issueId),
    ["pick-root"],
    "priority cards select only an actual issue before computing gains",
  );
  for (const limit of [1, 5, 100]) {
    const picks = await evaluate(page, `getTopKSet(${limit})`);
    assert.deepEqual(
      picks.items.map((i) => [i.issueId, i.marginal_gain, i.unblocked_issue_ids]),
      [["pick-root", 1, ["pick-child"]]],
      "missing prerequisite neither selected nor presumed completed",
    );
    assert.equal(picks.total_gain, 1);
    assert.equal(picks.open_nodes, 6, "candidate count excludes the synthetic endpoint");
    const impact = await evaluate(page, `topWhatIf(${limit})`);
    assert.deepEqual(
      impact.map((i) => [i.issueId, i.result.transitive_unblocks]),
      [["pick-root", 1]],
      "top cascade limit is applied after candidate eligibility",
    );
  }
  assert.deepEqual(await evaluate(page, "getTopKSet(0).items"), []);
  assert.deepEqual(await evaluate(page, "topWhatIf(0)"), []);
  assert.deepEqual(await evaluate(page, "getActionableIssues()"), ["pick-root"]);
  const raw = await evaluate(
    page,
    `(() => {const r=GRAPH_STATE.graph.topkSet(buildClosedSet(),5);return r.items.map(i=>[GRAPH_STATE.graph.nodeId(i.node),i.marginal_gain]);})()`,
  );
  assert.deepEqual(
    raw,
    [
      ["absent", 3],
      ["pick-root", 2],
    ],
    "unconstrained engine still sees the full graph, exposing the negative control",
  );
  assert.equal(
    await evaluate(page, `buildClosedSet()[GRAPH_STATE.nodeMap.get('absent')]`),
    0,
    "excluded prerequisite is still unresolved",
  );
  for (const selector of [
    'template[x-for*="topKSet?.items"] + div',
    'template[x-for*="topImpactIssues.slice"] + div',
  ]) {
    await click(page, selector);
    await waitFor(
      page,
      `${app}.selectedIssue?.id === 'pick-root'`,
      "suggestion card opens its actual issue",
    );
    await capture(page, "suggestion-detail");
    await key(page, "Escape");
    await click(page, 'a[href="#/insights"]');
  }
  assert.deepEqual(
    await evaluate(page, "[GRAPH_STATE.graph.nodeCount(), GRAPH_STATE.graph.edgeCount()]"),
    before,
    "selection and navigation preserve graph topology",
  );
  await capture(page, "suggestion-cards");
  clean(page);
  console.log(
    `PASS: ${page.name} visible candidate selection, true gains, readiness and card navigation`,
  );
}

// The seven-task fixture has root<-child<-leaf, child also depending on closed
// and tombstone prerequisites, held depending on root+other, and other related
// to root without blocking. All remaining statuses are open; IDs use sim-.
// An optional second bundle uses --pages-include-closed=false on the same source.
async function whatIfJourney(page, includeClosed = true) {
  await waitFor(
    page,
    `typeof Alpine !== 'undefined' && !${app}.loading && ${app}.stats.total === ${includeClosed ? 6 : 5} && ${app}.graphReady`,
    "visible simulation issues (resolved rows filtered) and real graph WASM",
  );
  await waitFor(page, "!!navigator.serviceWorker.controller", "simulation worker controls page");
  await delay(500);
  await send("Page.navigate", { url: origin + "/#/issue/sim-root" }, page.session);
  await waitFor(
    page,
    `typeof Alpine !== 'undefined' && ${app}.selectedIssue?.id === 'sim-root'`,
    "root issue details",
  );
  await click(page, "button", "Simulate Close");
  await waitFor(page, `${app}.whatIfResult !== null`, "actual simulation result");
  const result = await evaluate(page, `JSON.parse(JSON.stringify(${app}.whatIfResult))`);
  console.log("Observed root what-if:", JSON.stringify(result));
  assert.equal(result.direct_unblocks, 1, "closing root directly releases child only");
  assert.equal(result.transitive_unblocks, 2, "child completion then releases leaf");
  assert.deepEqual(result.cascade_issue_ids.sort(), ["sim-child", "sim-leaf"]);
  await waitFor(
    page,
    String.raw`[...document.querySelectorAll('p')].some(e=>(${visible})(e) && /Closing this issue would unblock 1 issue\(s\) and enable 2 downstream item\(s\)/.test(e.textContent.replace(/\s+/g,' ').trim()))`,
    "visible direct and transitive counts",
  );
  assert.equal(
    await evaluate(page, `whatIfClose('sim-leaf').direct_unblocks`),
    0,
    "leaf cannot release its prerequisites",
  );
  assert.equal(
    await evaluate(page, `whatIfClose('sim-other').direct_unblocks`),
    0,
    "held still needs root",
  );
  assert.equal(
    await evaluate(page, `whatIfClose('sim-deleted').transitive_unblocks`),
    0,
    "tombstone is already resolved",
  );
  assert.equal(
    await evaluate(page, `whatIfClose('sim-closed').transitive_unblocks`),
    0,
    "closed prerequisite remains resolved even when omitted",
  );
  assert.deepEqual(await evaluate(page, `[...getResolvedIssueIDs()].sort()`), [
    "sim-closed",
    "sim-deleted",
  ]);
  assert.deepEqual(await evaluate(page, `getActionableIssues().sort()`), ["sim-other", "sim-root"]);
  assert.deepEqual(
    await evaluate(page, `getTopKSet(5).items.map(i=>[i.issueId,i.marginal_gain])`),
    [
      ["sim-root", 2],
      ["sim-other", 1],
    ],
  );
  await capture(page, "what-if-detail");
  await key(page, "Escape");
  await click(page, 'a[href="#/insights"]');
  await waitFor(
    page,
    `[...document.querySelectorAll('span')].some(e=>(${visible})(e) && e.textContent.trim()==='3 potential unblocks')`,
    "visible combined priority gain",
  );
  await waitFor(
    page,
    `[...document.querySelectorAll('[x-text="item.marginal_gain"]')].filter(${visible}).map(e=>e.textContent).join(',')==='2,1'`,
    "visible per-pick gains",
  );
  await waitFor(
    page,
    `[...document.querySelectorAll('[x-text="item.result?.transitive_unblocks || 0"]')].filter(${visible}).map(e=>e.textContent).join(',')==='2'`,
    "visible cascade impact card",
  );
  await capture(page, "what-if-priorities");
  await click(page, 'a[href="#/graph"]');
  await waitFor(
    page,
    `${app}.forceGraphReady && !${app}.forceGraphLoading && !!document.querySelector('#graph-container canvas')`,
    "actual force graph",
  );
  const forceResult = await evaluate(
    page,
    `(()=>{const m=${app}.forceGraphModule; return m.performWhatIf(m.getGraph().graphData().nodes.find(n=>n.id==='sim-root'));})()`,
  );
  assert.equal(forceResult.direct_unblocks, 1);
  assert.equal(forceResult.transitive_unblocks, 2);
  await waitFor(
    page,
    `${app}.forceGraphModule.getWhatIfState().unblockedCount === 1`,
    "child animation executes",
  );
  // The graph uses a custom canvas renderer, not ForceGraph's linkColor accessor.
  // Draw each real link with that renderer into a real browser canvas and inspect
  // its pixels; alpha/antialiasing can round the theme's RGB by a few units.
  const greenLinks = await evaluate(
    page,
    `(()=>{
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
  })()`,
  );
  assert.deepEqual(
    greenLinks,
    includeClosed
      ? [
          ["sim-child", "sim-closed"],
          ["sim-child", "sim-root"],
        ]
      : [["sim-child", "sim-root"]],
    "visible resolved prerequisite edges glow in dependent-to-prerequisite direction",
  );
  await capture(page, "what-if-graph");
  await evaluate(
    page,
    `(()=>{const m=${app}.forceGraphModule; m.resetWhatIf(); m.performWhatIf(m.getGraph().graphData().nodes.find(n=>n.id==='sim-root')); m.resetWhatIf();})()`,
  );
  await delay(900);
  assert.deepEqual(
    await evaluate(page, `${app}.forceGraphModule.getWhatIfState()`),
    { active: false, sourceNode: null, unblockedCount: 0 },
    "cancelled cascade cannot revive itself",
  );
  assert.equal(
    await evaluate(
      page,
      `${app}.forceGraphModule.getGraph().graphData().nodes.some(n=>n._whatIfState)`,
    ),
    false,
    "reset leaves no stale node state",
  );
  await evaluate(
    page,
    `(()=>{const m=${app}.forceGraphModule; m.resetCriticalPath(); m.resetCycleNavigator(); const config=m.getConfig(); m.setConfig('linkWidth', config.linkWidth);})()`,
  );
  await evaluate(
    page,
    `(()=>{const m=${app}.forceGraphModule; m.performWhatIf(m.getGraph().graphData().nodes.find(n=>n.id==='sim-root')); const data=getGraphViewData(); m.loadData(data.issues,data.dependencies,null);})()`,
  );
  await delay(900);
  assert.deepEqual(
    await evaluate(page, `${app}.forceGraphModule.getWhatIfState()`),
    { active: false, sourceNode: null, unblockedCount: 0 },
    "data reload cancels a pending cascade",
  );
  await evaluate(
    page,
    `(()=>{const m=${app}.forceGraphModule; m.performWhatIf(m.getGraph().graphData().nodes.find(n=>n.id==='sim-root')); m.cleanup();})()`,
  );
  await delay(900);
  assert.deepEqual(
    await evaluate(page, `${app}.forceGraphModule.getWhatIfState()`),
    { active: false, sourceNode: null, unblockedCount: 0 },
    "cleanup cancels a pending cascade before freeing WASM",
  );
  clean(page);
  console.log(
    "PASS: directed what-if, resolved prerequisites, displayed gains, force-graph simulation and cancellation",
  );
}

async function journey(page, mobile) {
  await ready(page);
  await waitFor(
    page,
    "!!navigator.serviceWorker.controller",
    "installed offline service worker controls page",
  );
  // The first controller reloads the document. Wait for the resulting app.
  await delay(500);
  await ready(page);
  assert.deepEqual(
    await evaluate(page, "[GRAPH_STATE.graph.nodeCount(), GRAPH_STATE.graph.edgeCount()]"),
    [4, 1],
  );
  assert.deepEqual(
    await evaluate(page, "[...GRAPH_STATE.nodeMap.keys()].sort()"),
    ["browser-closed", "browser-detail", "browser-other", "browser-root"],
    "isolated issues populate the actual WASM graph",
  );
  await evaluate(
    page,
    `document.getElementById('priority-chart').scrollIntoView({block:'center'})`,
  );
  await waitFor(page, `Chart.getChart('priority-chart')?.width > 0`, "rendered chart");
  assert.deepEqual(
    await evaluate(page, `Chart.getChart('priority-chart').data.datasets[0].data`),
    [0, 2, 1, 0, 0],
  );
  assert.ok(
    await evaluate(
      page,
      `(() => { const c=document.getElementById('priority-chart'); return new Set(c.getContext('2d').getImageData(0,0,c.width,c.height).data).size > 10; })()`,
    ),
    "chart painted actual pixels",
  );
  await capture(page, "charts");
  await click(page, 'a[href="#/issues"]');
  await waitFor(
    page,
    `${app}.view === 'issues'`,
    "issue route has finished navigation before typing",
  );
  if (mobile) {
    // Existing icon control is exercised through the real pointer path.
    await click(page, 'button[aria-label="Search issues"]');
  }
  await search(page, "Orchid");
  await resultIDs(page, ["browser-root", "browser-detail", "browser-closed"]);
  if (mobile) await click(page, 'button[aria-label="Toggle filters"]');
  await click(page, "button", "open");
  await resultIDs(page, ["browser-root", "browser-detail"]);
  await capture(page, "filtered");
  const route = await evaluate(page, "location.href");
  await reload(page);
  assert.equal(await evaluate(page, "location.href"), route, "filter route survives reload");
  await resultIDs(page, ["browser-root", "browser-detail"]);
  // Keyboard activation must open the same issue as pointer activation.
  await evaluate(
    page,
    `document.querySelector('[aria-label^="View issue browser-detail:"]').focus()`,
  );
  await key(page, "Enter");
  await waitFor(page, `${app}.selectedIssue?.id === 'browser-detail'`, "keyboard issue detail");
  assert.ok(
    await evaluate(
      page,
      `document.body.innerText.includes('Verified comment violet') && document.body.innerText.includes('Reviewer')`,
    ),
    "actual exported comment shown",
  );
  assert.equal(
    await evaluate(page, "window.__unsafeDisplay || false"),
    false,
    "unsafe display string never executes",
  );
  assert.equal(
    await evaluate(
      page,
      `document.querySelector('[x-html="renderMarkdown(selectedIssue.description)"]').querySelectorAll('script,[onerror],[onclick]').length`,
    ),
    0,
    "unsafe nodes/handlers stripped before rendering",
  );
  await click(page, "button", "Copy link");
  await waitFor(page, `${app}.copyLinkMessage === 'Link copied'`, "copy feedback");
  assert.equal(
    await evaluate(page, "navigator.clipboard.readText()"),
    origin + "/#/issue/browser-detail",
  );
  await capture(page, "detail-copy");
  await click(page, "button", "Show Graph");
  await waitFor(
    page,
    `!!document.querySelector('[x-ref="depGraph"] svg')`,
    "real Mermaid dependency SVG",
  );
  assert.ok(
    await evaluate(
      page,
      `document.querySelector('[x-ref="depGraph"]').textContent.includes('browser-root')`,
    ),
    "Mermaid contains actual blocker ID",
  );
  await capture(page, "mermaid");
  await key(page, "Escape");
  await waitFor(page, `!${app}.selectedIssue`, "Escape closes detail");
  await click(page, 'a[href="#/graph"]');
  await waitFor(
    page,
    `${app}.forceGraphReady && !${app}.forceGraphLoading && !!document.querySelector('#graph-container canvas')`,
    "interactive graph rendered",
  );
  const graphIDs = await evaluate(
    page,
    `${app}.forceGraphModule.getGraph().graphData().nodes.map(n=>n.id).sort()`,
  );
  assert.deepEqual(graphIDs, ["browser-closed", "browser-detail", "browser-other", "browser-root"]);
  // Click an actual force-graph node using its rendered graph-to-screen transform.
  await delay(1000);
  const point = await evaluate(
    page,
    `(() => { const g=${app}.forceGraphModule.getGraph(); const n=g.graphData().nodes.find(n=>n.id==='browser-root'); const p=g.graph2ScreenCoords(n.x,n.y); const r=document.querySelector('#graph-container canvas').getBoundingClientRect(); return {x:r.x+p.x,y:r.y+p.y}; })()`,
  );
  for (const type of ["mousePressed", "mouseReleased"])
    await send(
      "Input.dispatchMouseEvent",
      { type, ...point, button: "left", clickCount: 1 },
      page.session,
    );
  await waitFor(
    page,
    `${app}.graphDetailNode?.id === 'browser-root'`,
    "graph pointer selects actual root node",
  );
  await capture(page, "graph");
  assert.ok(
    await evaluate(page, "document.documentElement.scrollWidth <= innerWidth + 1"),
    "no horizontal loss at viewport width",
  );
  await send("Page.navigate", { url: origin + "/#/issue/browser-detail" }, page.session);
  await ready(page);
  await waitFor(page, `${app}.selectedIssue?.id === 'browser-detail'`, "direct issue route");
  await setOffline(page, true);
  await reload(page);
  await waitFor(
    page,
    `${app}.selectedIssue?.id === 'browser-detail'`,
    "primed offline route/detail preserved",
  );
  assert.ok(
    await evaluate(page, `document.body.innerText.includes('Verified comment violet')`),
    "comment still visible offline",
  );
  await capture(page, "offline-detail");
  await setOffline(page, false);
  // A genuinely changed service-worker response triggers browser update/install.
  workerRevision++;
  await evaluate(
    page,
    `window.__beforeWorkerUpdate = true; navigator.serviceWorker.getRegistration().then(r=>r.update())`,
  );
  await waitFor(page, "!window.__beforeWorkerUpdate", "updated service worker reloads client");
  await ready(page);
  await waitFor(
    page,
    `${app}.selectedIssue?.id === 'browser-detail'`,
    "route preserved after service worker update",
  );
  await capture(page, "updated");
  clean(page);
  console.log(
    `ok: ${page.name} charts/search/filter/keyboard/detail/comments/copy/Mermaid/graph/offline/update`,
  );
}
try {
  const stderr = fs.createWriteStream(path.join(artifacts, "chrome.stderr"));
  chrome = spawn(
    browser,
    [
      "--headless=new",
      "--no-sandbox",
      "--disable-gpu",
      "--disable-dev-shm-usage",
      "--no-first-run",
      "--disable-background-networking",
      "--disable-sync",
      "--disable-component-update",
      "--remote-debugging-port=0",
      `--user-data-dir=${path.join(artifacts, "profile")}`,
      "--user-agent=OpenAI File Downloader, XaiImageApiFetch/1.0",
      "about:blank",
    ],
    { stdio: ["ignore", "ignore", "pipe"] },
  );
  const endpoint = await new Promise((resolve, reject) => {
    let output = "";
    const timer = setTimeout(() => reject(new Error("Chrome DevTools endpoint missing")), 15000);
    chrome.on("error", reject);
    chrome.stderr.on("data", (chunk) => {
      stderr.write(chunk);
      output += chunk;
      const match = output.match(/DevTools listening on (ws:\/\/[^\s]+)/);
      if (match) {
        clearTimeout(timer);
        resolve(match[1]);
      }
    });
  });
  socket = new WebSocket(endpoint);
  await new Promise((resolve, reject) => {
    socket.onopen = resolve;
    socket.onerror = reject;
  });
  socket.onmessage = ({ data }) => {
    const message = JSON.parse(data);
    if (message.id) {
      const entry = pending.get(message.id);
      if (!entry) return;
      pending.delete(message.id);
      clearTimeout(entry.timer);
      if (message.error) entry.reject(new Error(JSON.stringify(message.error)));
      else entry.resolve(message.result);
      return;
    }
    if (message.method === "Target.detachedFromTarget") {
      records.push({ detachedTarget: message });
      const detached = sessions.get(message.params.sessionId);
      if (detached) detached.detached = true;
    }
    if (message.method === "Target.attachedToTarget") records.push({ attachedTarget: message });
    const page =
      sessions.get(message.sessionId) ||
      (message.method === "Target.attachedToTarget"
        ? [...sessions.values()].find(
            (p) => !p.worker && p.context === message.params.targetInfo.browserContextId,
          )
        : undefined);
    if (!page) {
      records.push({ unassignedEvent: message });
      return;
    }
    if (message.method === "Target.attachedToTarget") {
      const child = {
        ...page,
        name: page.name + ":" + message.params.targetInfo.type,
        session: message.params.sessionId,
        target: message.params.targetInfo.targetId,
        worker: true,
        owner: page.owner || page,
      };
      sessions.set(child.session, child);
      (async () => {
        // Queue domains before resuming. Runtime.enable's response may itself
        // wait for the paused worker to start, so do not await it in isolation.
        await Promise.all([
          send("Runtime.enable", {}, child.session),
          send("Network.enable", {}, child.session),
          emulateRequests(child, !!child.owner.offline),
          send("Fetch.enable", { patterns: [{ urlPattern: "*" }] }, child.session),
          send("Runtime.runIfWaitingForDebugger", {}, child.session),
        ]);
      })().catch(async (err) => {
        // A worker whose script cannot be fetched can disappear before its
        // debugger domains finish enabling. A confirmed detached target
        // cannot execute unobserved code; preserve that lifecycle evidence.
        if (
          /Inspected target navigated or closed|Session with given id not found/.test(err.message)
        ) {
          const { targetInfos } = await send("Target.getTargets");
          if (!targetInfos.some((t) => t.targetId === child.target)) {
            child.detached = true;
            records.push({
              closedBeforeMonitor: err.message,
              page: child.name,
              sessionId: child.session,
              absentTarget: child.target,
            });
            return;
          }
        }
        monitorErrors.push(err.message);
        records.push({ workerMonitorError: err.message, page: child.name });
        try {
          await send("Runtime.runIfWaitingForDebugger", {}, child.session);
        } catch (resumeError) {
          monitorErrors.push(resumeError.message);
        }
      });
      return;
    }
    if (
      [
        "Runtime.consoleAPICalled",
        "Runtime.exceptionThrown",
        "Log.entryAdded",
        "Network.requestWillBeSent",
        "Network.responseReceived",
        "Network.loadingFailed",
        "ServiceWorker.workerErrorReported",
        "ServiceWorker.workerVersionUpdated",
      ].includes(message.method)
    ) {
      records.push({
        page: page.name,
        offline: !!(page.owner || page).offline,
        originListening: server.listening,
        ...message,
      });
    }
    if (
      message.method === "Runtime.exceptionThrown" ||
      (message.method === "Log.entryAdded" &&
        /Content Security Policy|Refused to|violates.*policy/i.test(message.params.entry.text))
    )
      page.errors.push(message.params);
    if (message.method === "Fetch.requestPaused") {
      const url = message.params.request.url;
      const allowed = url.startsWith(origin + "/") || /^(data:|blob:|about:)/.test(url);
      if (!allowed) page.external.push(url);
      const offlineWorker = page.worker && page.owner.offline;
      // Explicitly fail worker fetches as a second guard against target-local
      // emulation gaps. CacheStorage reads do not issue these network requests.
      const failReason = !allowed
        ? "BlockedByClient"
        : offlineWorker
          ? "InternetDisconnected"
          : null;
      send(
        failReason ? "Fetch.failRequest" : "Fetch.continueRequest",
        { requestId: message.params.requestId, ...(failReason ? { errorReason: failReason } : {}) },
        page.session,
      ).catch((err) => records.push({ fetchError: err.message }));
    }
  };
  records.push({ browser: await send("Browser.getVersion"), origin, mode });
  if (projectBundle) {
    activeBundle = projectBundle;
    const project = await openPage("project");
    await waitFor(
      project,
      `typeof Alpine !== 'undefined' && !${app}.loading && Number.isInteger(${app}.stats.total) && !${app}.error && !${app}.globalError`,
      "real project export boot",
    );
    await waitFor(
      project,
      "!!navigator.serviceWorker.controller",
      "project offline worker installed",
    );
    await delay(500);
    await waitFor(
      project,
      `typeof Alpine !== 'undefined' && !${app}.loading && Number.isInteger(${app}.stats.total) && !${app}.error && !${app}.globalError`,
      "project boot after worker activation",
    );
    await capture(project, "boot");
    clean(project);
    console.log("ok: requested project export boot (known fixture journeys follow)");
    activeBundle = bundle;
  }
  const desktop = await openPage("desktop");
  if (mode === "graph-startup") {
    await graphStartupJourney(desktop);
    await send("Target.closeTarget", { targetId: desktop.target });
    await graphStartupJourney(await openPage("mobile-360", 360));
  } else if (mode === "timeline-performance") {
    await timelinePerformanceJourney(desktop);
    await send("Target.closeTarget", { targetId: desktop.target });
    await timelinePerformanceJourney(await openPage("mobile-360", 360));
  } else if (mode === "precomputed-metrics") {
    await precomputedMetricsJourney(desktop);
    await precomputedMetricsJourney(await openPage("mobile-360", 360));
  } else if (mode === "timeline-sprints") {
    await timelineSprintsJourney(desktop);
    await timelineSprintsJourney(await openPage("mobile-360", 360));
  } else if (mode === "timeline-animation") {
    await timelineAnimationJourney(desktop);
    await timelineAnimationJourney(await openPage("mobile-360", 360));
  } else if (mode === "timeline-removal") {
    await timelineRemovalJourney(desktop);
    await timelineRemovalJourney(await openPage("mobile-360", 360));
  } else if (mode === "timeline-baseline") {
    await timelineBaselineJourney(desktop);
    await timelineBaselineJourney(await openPage("mobile-360", 360));
  } else if (mode === "timeline-controls") {
    await timelineControlsJourney(desktop);
    await timelineControlsJourney(await openPage("mobile-360", 360));
  } else if (mode === "timeline") {
    await timelineJourney(desktop);
    await timelineJourney(await openPage("mobile-360", 360));
  } else if (mode === "history-loading") {
    await historyLoadingJourney(desktop);
    await historyLoadingJourney(await openPage("mobile-360", 360));
  } else if (mode === "graph-reload") {
    await graphReloadJourney(desktop);
    await graphReloadJourney(await openPage("mobile-360", 360));
  } else if (mode === "layout-seeds") {
    await layoutSeedsJourney(desktop);
    await layoutSeedsJourney(await openPage("mobile-360", 360));
    if (updatedBundle) {
      activeBundle = updatedBundle;
      await layoutSeedsJourney(await openPage("edgeless"), true);
    }
  } else if (mode === "blocking-types") {
    await blockingTypesJourney(desktop);
  } else if (mode === "readiness") {
    await readinessJourney(desktop);
    await readinessJourney(await openPage("mobile-360", 360));
  } else if (mode === "hits") {
    await hitsJourney(desktop);
    await hitsJourney(await openPage("mobile-360", 360));
  } else if (mode === "suggestion-visibility") {
    await suggestionVisibilityJourney(desktop);
    await suggestionVisibilityJourney(await openPage("mobile-360", 360));
  } else if (mode === "metric-visibility") {
    await metricVisibilityJourney(desktop);
    await metricVisibilityJourney(await openPage("mobile-360", 360));
    if (updatedBundle) {
      activeBundle = updatedBundle;
      const empty = await openPage("empty-ranking");
      await waitFor(
        empty,
        `typeof Alpine !== 'undefined' && !${app}.loading && ${app}.stats.total === 0 && ${app}.graphReady`,
        "empty graph initialized",
      );
      for (const fn of [
        "getTopByHITSAuth",
        "getTopByHITSHub",
        "getTopByKCore",
        "getTopByBetweenness",
        "getTopByCriticalPath",
        "getIssuesBySlack",
      ]) {
        assert.deepEqual(
          await evaluate(empty, `${fn}(10)`),
          [],
          `${fn}: actual empty export has no ranked issues`,
        );
      }
      assert.deepEqual(await evaluate(empty, "getActionableIssues()"), []);
      assert.deepEqual(await evaluate(empty, "topWhatIf(10)"), []);
      assert.deepEqual(await evaluate(empty, "getTopKSet(5).items"), []);
      clean(empty);
      console.log("PASS: empty export metric queries");
    }
  } else if (mode === "what-if") {
    await whatIfJourney(desktop);
    if (updatedBundle) {
      activeBundle = updatedBundle;
      await whatIfJourney(await openPage("closed-rows-excluded"), false);
    }
  } else if (mode === "offline-only") {
    await ready(desktop);
    await capture(desktop, "first-load");
    await setOffline(desktop, true);
    await reload(desktop);
    await capture(desktop, "offline");
    clean(desktop);
    console.log("INCOMPLETE: focused offline check succeeded; full journeys were not run");
    process.exitCode = 2;
  } else {
    await journey(desktop, false);
    const mobile = await openPage("mobile-360", 360);
    await journey(mobile, true);
    // Exercise the genuine optional-module import failure, without replacing
    // the loader or scorer. Small fixtures normally take the size threshold.
    await evaluate(desktop, `HYBRID_WASM_STATE.attempted = false; initHybridWasmScorer(5000)`);
    assert.equal(await evaluate(desktop, "getHybridWasmStatus().ready"), false);
    assert.match(await evaluate(desktop, "getHybridWasmStatus().reason"), /fetch|import|module/i);
    await key(desktop, "Escape");
    await search(desktop, "Orchid");
    await evaluate(
      desktop,
      `(() => {const e=[...document.querySelectorAll('select[x-model="searchMode"]')].find(${visible}); e.value='hybrid'; e.dispatchEvent(new Event('change',{bubbles:true}));})()`,
    );
    await waitFor(
      desktop,
      `${app}.issues.length > 0 && ${app}.issues.every(i=>Number.isFinite(i.hybrid_score))`,
      "JS hybrid results after optional module failure",
    );
    await resultIDs(desktop, ["browser-root", "browser-detail", "browser-closed"]);
    assert.equal(
      await evaluate(desktop, `${app}.searchBackend`),
      "substring",
      "actual vendored SQLite capability disclosed",
    );
    assert.ok(
      await evaluate(desktop, `document.body.innerText.includes('Substring text matching')`),
    );
    const scored = await evaluate(
      desktop,
      `JSON.parse(JSON.stringify(${app}.issues.map(i=>({score:i.hybrid_score,text:i.text_score,components:i.component_scores}))))`,
    );
    // "Orchid" is a short query: text receives .55 and each remaining default
    // weight is scaled by .45/.60. Recompute without invoking the scorer.
    for (const row of scored) {
      const expected =
        0.55 * row.text +
        0.15 * row.components.pagerank +
        0.1125 * row.components.status +
        0.075 * row.components.impact +
        0.075 * row.components.priority +
        0.0375 * row.components.recency;
      assert.ok(
        Math.abs(row.score - expected) < 1e-12,
        `fallback applies the selected hybrid weights: ${JSON.stringify({ row, expected })}`,
      );
    }
    await capture(desktop, "optional-hybrid-fallback");
    clean(desktop);
    await click(desktop, '[aria-label^="View issue browser-detail:"]');
    await waitFor(
      desktop,
      `${app}.selectedIssue?.id === 'browser-detail'`,
      "detail for denied clipboard",
    );
    await send("Browser.setPermission", {
      permission: { name: "clipboard-write" },
      setting: "denied",
      origin,
      browserContextId: desktop.context,
    });
    await click(desktop, "button", "Copy link");
    await waitFor(
      desktop,
      `${app}.copyLinkMessage.startsWith('Could not copy.')`,
      "clipboard denial has useful visible feedback",
    );
    await capture(desktop, "clipboard-denied");
    assert.ok(updatedBundle, "second real export required for changed-data update");
    const oldCaches = await evaluate(desktop, "caches.keys()");
    await evaluate(
      desktop,
      `caches.open('unrelated-browser-journey').then(c=>c.put('/sentinel',new Response('keep me')))`,
    );
    activeBundle = updatedBundle;
    await evaluate(
      desktop,
      `window.__beforeDataUpdate = true; navigator.serviceWorker.getRegistration().then(r=>r.update())`,
    );
    await waitFor(
      desktop,
      "!window.__beforeDataUpdate",
      "new exported bundle replaces service worker",
    );
    await ready(desktop);
    await waitFor(
      desktop,
      `${app}.selectedIssue?.title === 'Orchid searchable detail updated'`,
      "new exported database replaces old OPFS cache",
    );
    await setOffline(desktop, true);
    await reload(desktop);
    await waitFor(
      desktop,
      `${app}.selectedIssue?.title === 'Orchid searchable detail updated'`,
      "new data survives offline reload",
    );
    assert.equal(
      await evaluate(
        desktop,
        `caches.open('unrelated-browser-journey').then(c=>c.match('/sentinel')).then(r=>r.text())`,
      ),
      "keep me",
      "activation preserves unrelated browser cache",
    );
    const newCaches = await evaluate(desktop, "caches.keys()");
    assert.ok(
      oldCaches.filter((k) => k.startsWith("beads-viewer-")).every((k) => !newCaches.includes(k)),
      "successful activation retires obsolete bundle cache",
    );
    await capture(desktop, "updated-export-offline");
    clean(desktop);
    // A corrupt update must retain the currently working bundle and database.
    await setOffline(desktop, false);
    changedAsset = "/charts.js";
    workerRevision++;
    const beforeFailedUpdate = records.length;
    await evaluate(desktop, `navigator.serviceWorker.getRegistration().then(r=>r.update())`);
    const updateFailureDeadline = Date.now() + 25000;
    while (
      Date.now() < updateFailureDeadline &&
      !records
        .slice(beforeFailedUpdate)
        .some(
          (r) =>
            r.method === "ServiceWorker.workerErrorReported" &&
            JSON.stringify(r.params).includes("Offline asset changed: charts.js"),
        )
    )
      await delay(100);
    assert.ok(
      records
        .slice(beforeFailedUpdate)
        .some(
          (r) =>
            r.method === "ServiceWorker.workerErrorReported" &&
            JSON.stringify(r.params).includes("Offline asset changed: charts.js"),
        ),
      "corrupt update reports exact installer failure",
    );
    await setOffline(desktop, true);
    await reload(desktop);
    await waitFor(
      desktop,
      `${app}.selectedIssue?.title === 'Orchid searchable detail updated'`,
      "failed update preserves last working bundle offline",
    );
    await capture(desktop, "failed-update-keeps-working-bundle");
    changedAsset = "";
    activeBundle = bundle;
    const unprimed = await openPage("negative-unprimed-offline", 1280, true);
    await delay(1000);
    assert.equal(
      await evaluate(unprimed, `typeof Alpine !== 'undefined' && ${app}.stats.total === 4`),
      false,
      "unprimed offline cannot boot",
    );
    await capture(unprimed, "expected-failure");
    await setOffline(unprimed, false);
    brokenAsset = "/vendor/sql-wasm.wasm";
    assert.ok(
      fs.existsSync(path.join(bundle, brokenAsset)),
      "negative breaks an actual required SQL asset",
    );
    const missing = await openPage("negative-required-asset");
    await waitFor(
      missing,
      `typeof Alpine !== 'undefined' && !${app}.loading && !!(${app}.error || ${app}.globalError)`,
      "required SQL WASM failure is visible",
    );
    assert.equal(await evaluate(missing, `${app}.graphReady`), false);
    assert.equal(
      await evaluate(missing, "!!navigator.serviceWorker.controller"),
      false,
      "incomplete bundle never activates worker",
    );
    await capture(missing, "expected-failure");
    brokenAsset = "";
    changedAsset = "/charts.js";
    const changed = await openPage("negative-changed-after-export");
    await ready(changed);
    const failureDeadline = Date.now() + 25000;
    while (
      Date.now() < failureDeadline &&
      !records.some(
        (r) =>
          r.page === changed.name &&
          r.method === "ServiceWorker.workerErrorReported" &&
          JSON.stringify(r.params).includes("Offline asset changed: charts.js"),
      )
    )
      await delay(100);
    assert.ok(
      records.some(
        (r) =>
          r.page === changed.name &&
          r.method === "ServiceWorker.workerErrorReported" &&
          JSON.stringify(r.params).includes("Offline asset changed: charts.js"),
      ),
      "browser reports exact changed-asset install failure",
    );
    assert.equal(
      await evaluate(changed, "!!navigator.serviceWorker.controller"),
      false,
      "changed asset prevents offline activation",
    );
    await capture(changed, "expected-incomplete-offline");
    for (const page of sessions.values())
      assert.deepEqual(page.external, [], `${page.name}: no external network reliance`);
    assert.deepEqual(
      monitorErrors,
      [],
      "all browser/worker monitors remained active through negative controls",
    );
    assert.ok(
      records.some(
        (r) => r.page?.includes(":service_worker") && r.method === "Network.requestWillBeSent",
      ),
      "real worker network requests were observed",
    );
    const offlineWorkerRequests = new Set(
      records
        .filter(
          (r) =>
            r.offline &&
            r.page?.includes(":service_worker") &&
            r.method === "Network.requestWillBeSent",
        )
        .map((r) => r.sessionId + ":" + r.params.requestId),
    );
    assert.ok(
      offlineWorkerRequests.size > 0,
      "offline optional-resource fetch control actually reached the worker",
    );
    assert.equal(
      records.filter(
        (r) =>
          r.method === "Network.responseReceived" &&
          offlineWorkerRequests.has(r.sessionId + ":" + r.params.requestId) &&
          r.params.response.status < 400,
      ).length,
      0,
      "offline worker never repairs missing cache entries over the network",
    );
    console.log(
      "PASS: real desktop/mobile/offline/update journeys and required/optional/unsafe/unprimed controls",
    );
  }
} catch (err) {
  for (const page of sessions.values()) await capture(page, "failure");
  console.error(err.stack);
  process.exitCode = 1;
} finally {
  if (monitorErrors.length) console.error("Worker monitor errors:", monitorErrors);
  records.push({
    pendingCommands: [...pending.values()].map(({ method, sessionId }) => ({ method, sessionId })),
  });
  fs.writeFileSync(path.join(artifacts, "browser-events.json"), JSON.stringify(records, null, 2));
  if (socket?.readyState === WebSocket.OPEN) {
    try {
      await send("Browser.close");
    } catch (err) {
      console.error(err.message);
    }
    socket.close();
  }
  for (const entry of pending.values()) clearTimeout(entry.timer);
  chrome?.kill("SIGTERM");
  await new Promise((resolve) => server.close(resolve));
  console.log(`Browser artifacts: ${artifacts}`);
}
