/**
 * Cross-Origin Isolation Service Worker
 *
 * This service worker adds COOP and COEP headers to enable SharedArrayBuffer
 * on hosting platforms that don't allow setting response headers (like GitHub Pages).
 *
 * SharedArrayBuffer is required by sql.js WASM for optimal performance.
 *
 * Based on: https://github.com/nicobrinkkemper/coi-serviceworker
 * License: MIT
 */

// Replaced by CopyEmbeddedAssets with hashes of the actual exported files.
const OFFLINE_ASSETS = [];
const CACHE_REVISION = 'development';
const CACHE_PREFIX = `beads-viewer-${self.registration.scope}-`;
const CACHE_NAME = CACHE_PREFIX + CACHE_REVISION;

function cacheKey(request) {
  const url = new URL(request.url || request, self.registration.scope);
  // These parameters only defeat intermediary caches; file identity is bound
  // above. Preserve other query parameters and separate Pages project scopes.
  url.searchParams.delete('_t');
  url.searchParams.delete('v');
  if (url.pathname.endsWith('/')) url.pathname += 'index.html';
  return url.href;
}

// Headers needed for cross-origin isolation
// Using 'credentialless' instead of 'require-corp' to allow CDN resources
// while still enabling SharedArrayBuffer for sql.js WASM performance.
// 'credentialless' allows cross-origin resources without credentials (cookies).
const COI_HEADERS = {
  'Cross-Origin-Embedder-Policy': 'credentialless',
  'Cross-Origin-Opener-Policy': 'same-origin',
};

/**
 * Check if the request should have COI headers added
 */
function shouldAddHeaders(request) {
  // Only add headers to same-origin requests
  const url = new URL(request.url);
  if (url.origin !== self.location.origin) {
    return false;
  }

  // Add headers to HTML and JS files
  const pathname = url.pathname;
  if (
    pathname.endsWith('.html') ||
    pathname.endsWith('.js') ||
    pathname.endsWith('/') ||
    pathname === ''
  ) {
    return true;
  }

  // Check accept header for HTML requests
  const accept = request.headers.get('Accept') || '';
  if (accept.includes('text/html')) {
    return true;
  }

  return false;
}

/**
 * Add COI headers to a response
 */
function addCOIHeaders(response) {
  // Clone the response and add headers
  const newHeaders = new Headers(response.headers);

  for (const [key, value] of Object.entries(COI_HEADERS)) {
    newHeaders.set(key, value);
  }

  return new Response(response.body, {
    status: response.status,
    statusText: response.statusText,
    headers: newHeaders,
  });
}

// Install event
self.addEventListener('install', (event) => {
  console.log('[COI-SW] Installing service worker');
  event.waitUntil((async () => {
    if (!OFFLINE_ASSETS.length) throw new Error('Offline asset manifest missing');
    const cache = await caches.open(CACHE_NAME);
    for (const asset of OFFLINE_ASSETS) {
      const url = new URL(asset.path, self.registration.scope);
      const response = await fetch(url, { cache: 'no-store' });
      if (!response.ok) throw new Error(`Offline asset unavailable: ${asset.path}`);
      const bytes = await response.clone().arrayBuffer();
      const digest = await crypto.subtle.digest('SHA-256', bytes);
      const hash = [...new Uint8Array(digest)].map(v => v.toString(16).padStart(2, '0')).join('');
      if (hash !== asset.sha256) throw new Error(`Offline asset changed: ${asset.path}`);
      await cache.put(cacheKey(url.href), response);
    }
    console.log('[COI-SW] Complete offline bundle cached:', CACHE_REVISION);
    await self.skipWaiting();
  })());
});

// Activate event
self.addEventListener('activate', (event) => {
  console.log('[COI-SW] Activating service worker');
  event.waitUntil((async () => {
    // Activation is only reached after every required asset was verified.
    // Retire this scope's obsolete bundles, preserving unrelated application
    // caches and the previous working bundle whenever installation fails.
    for (const name of await caches.keys()) {
      if (name.startsWith(CACHE_PREFIX) && name !== CACHE_NAME) await caches.delete(name);
    }
    await self.clients.claim();
  })());
});

// Fetch event - intercept requests and add COI headers
self.addEventListener('fetch', (event) => {
  const request = event.request;

  // Only process GET requests
  if (request.method !== 'GET') {
    return;
  }

  const url = new URL(request.url);
  if (url.origin !== self.location.origin || !url.href.startsWith(self.registration.scope)) {
    return;
  }

  event.respondWith(
    (async () => {
      try {
        const cache = await caches.open(CACHE_NAME);
        const key = cacheKey(request);
        // The active worker only serves its complete, hash-checked bundle.
        // A new export installs separately and switches clients after priming.
        let response = await cache.match(key);
        if (!response) {
          try {
            response = await fetch(request);
          } catch (error) {
            // An uncached optional resource may be unavailable offline. Return
            // a failed network response, without an unhandled worker promise
            // or invented success content. Other errors still surface below.
            if (!(error instanceof TypeError)) throw error;
            console.warn('[COI-SW] Resource unavailable:', request.url);
            return Response.error();
          }
          // Optional late-written data (history) remains available offline
          // after use. Failed/opaque responses never replace cached content.
          if (response.ok && response.type !== 'opaque') await cache.put(key, response.clone());
        }

        // Check if response is ok and we can modify it
        if (!response.ok || response.type === 'opaque') {
          return response;
        }

        // Add COI headers
        return shouldAddHeaders(request) ? addCOIHeaders(response) : response;
      } catch (error) {
        console.error('[COI-SW] Fetch error:', error);
        throw error;
      }
    })()
  );
});

// Message handler for control messages
self.addEventListener('message', (event) => {
  if (event.data === 'skipWaiting') {
    self.skipWaiting();
  }

  if (event.data === 'checkCOI') {
    event.ports[0].postMessage({
      crossOriginIsolated: self.crossOriginIsolated,
      coepHeader: COI_HEADERS['Cross-Origin-Embedder-Policy'],
      coopHeader: COI_HEADERS['Cross-Origin-Opener-Policy'],
    });
  }
});

console.log('[COI-SW] Service worker loaded');
