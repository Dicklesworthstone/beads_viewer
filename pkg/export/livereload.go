// Package export provides data export functionality for bv.
//
// This file implements live-reload via Server-Sent Events (SSE) for the preview server.
// When files change in the bundle directory, connected browsers receive reload events.
package export

import (
	"bytes"
	"fmt"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// LiveReloadHub manages SSE connections and file watching for live-reload.
type LiveReloadHub struct {
	bundlePath string
	watcher    *fsnotify.Watcher

	// clients holds all connected SSE clients
	mu      sync.RWMutex
	clients map[chan struct{}]struct{}

	// shutdown signal
	done     chan struct{}
	stopOnce sync.Once

	// debounce rapid file changes
	lastEvent time.Time
	debounce  time.Duration
}

// NewLiveReloadHub creates a new live-reload hub for the given bundle path.
func NewLiveReloadHub(bundlePath string) (*LiveReloadHub, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("create fsnotify watcher: %w", err)
	}

	hub := &LiveReloadHub{
		bundlePath: bundlePath,
		watcher:    watcher,
		clients:    make(map[chan struct{}]struct{}),
		done:       make(chan struct{}),
		debounce:   200 * time.Millisecond,
	}

	return hub, nil
}

// Start begins watching the bundle directory for changes.
func (h *LiveReloadHub) Start() error {
	// Watch the bundle directory
	if err := h.watcher.Add(h.bundlePath); err != nil {
		return fmt.Errorf("watch bundle path: %w", err)
	}

	// Also watch subdirectories (one level deep for common cases)
	entries, err := filepath.Glob(filepath.Join(h.bundlePath, "*"))
	if err == nil {
		for _, entry := range entries {
			// Best effort - ignore errors for individual subdirs
			_ = h.watcher.Add(entry)
		}
	}

	go h.watchLoop()
	return nil
}

// Stop shuts down the live-reload hub.
func (h *LiveReloadHub) Stop() {
	h.stopOnce.Do(func() {
		close(h.done)
		_ = h.watcher.Close()

		// Close all client channels
		h.mu.Lock()
		defer h.mu.Unlock()
		for ch := range h.clients {
			close(ch)
		}
		h.clients = make(map[chan struct{}]struct{})
	})
}

// ClientCount returns the number of connected clients.
func (h *LiveReloadHub) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// watchLoop processes file system events and notifies clients.
func (h *LiveReloadHub) watchLoop() {
	for {
		select {
		case <-h.done:
			return

		case event, ok := <-h.watcher.Events:
			if !ok {
				return
			}

			// Only notify on write/create events (not chmod, etc)
			if event.Op&(fsnotify.Write|fsnotify.Create) == 0 {
				continue
			}

			// Debounce rapid changes
			now := time.Now()
			if now.Sub(h.lastEvent) < h.debounce {
				continue
			}
			h.lastEvent = now

			h.notifyClients()

		case _, ok := <-h.watcher.Errors:
			if !ok {
				return
			}
			// Errors are logged but don't stop the watcher
		}
	}
}

// notifyClients sends a reload signal to all connected SSE clients.
func (h *LiveReloadHub) notifyClients() {
	h.mu.RLock()
	defer h.mu.RUnlock()

	for ch := range h.clients {
		select {
		case ch <- struct{}{}:
		default:
			// Client not ready, skip (non-blocking)
		}
	}
}

// SSEHandler returns an HTTP handler for the SSE endpoint.
func (h *LiveReloadHub) SSEHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		// Set SSE headers
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		if r.Method == http.MethodHead {
			return
		}

		// Ensure we can flush
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "SSE not supported", http.StatusInternalServerError)
			return
		}

		// Register this client
		clientCh := make(chan struct{}, 1)
		h.addClient(clientCh)

		// Cleanup on disconnect
		defer func() {
			h.removeClient(clientCh)
		}()

		// Send initial connection event
		fmt.Fprintf(w, "event: connected\ndata: {\"status\":\"connected\"}\n\n")
		flusher.Flush()

		// Keep connection open and send events
		for {
			select {
			case <-r.Context().Done():
				return
			case <-h.done:
				return
			case _, ok := <-clientCh:
				if !ok {
					return
				}
				fmt.Fprintf(w, "event: reload\ndata: {\"action\":\"reload\"}\n\n")
				flusher.Flush()
			}
		}
	}
}

func (h *LiveReloadHub) addClient(ch chan struct{}) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clients[ch] = struct{}{}
}

func (h *LiveReloadHub) removeClient(ch chan struct{}) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.clients, ch)
}

// LiveReloadScriptPath is where the preview server serves the live-reload
// script. It is a same-origin file (not an inline block) because the exported
// dashboard's Content-Security-Policy forbids inline scripts; injecting an
// inline <script> would be refused by the browser and live reload would
// silently never connect.
const LiveReloadScriptPath = "/__preview__/livereload.js"

// LiveReloadScriptTag is the tag injected before </body> in HTML responses.
const LiveReloadScriptTag = `<script src="` + LiveReloadScriptPath + `"></script>`

// LiveReloadScript is the JavaScript served at LiveReloadScriptPath: it
// connects to the SSE endpoint and reloads the page on events.
const LiveReloadScript = `(function() {
  if (typeof(EventSource) === 'undefined') return;
  var reconnectDelay = 1000;
  var maxReconnectDelay = 30000;

  function connect() {
    var es = new EventSource('/__preview__/events');

    es.addEventListener('connected', function() {
      console.log('[bv] Live reload connected');
      reconnectDelay = 1000; // Reset delay on successful connect
    });

    es.addEventListener('reload', function() {
      console.log('[bv] Reloading...');
      location.reload();
    });

    es.onerror = function() {
      es.close();
      console.log('[bv] Live reload disconnected, reconnecting in ' + (reconnectDelay/1000) + 's...');
      setTimeout(connect, reconnectDelay);
      reconnectDelay = Math.min(reconnectDelay * 2, maxReconnectDelay);
    };
  }

  connect();
})();
`

// liveReloadScriptHandler serves LiveReloadScript as a JavaScript file.
func liveReloadScriptHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write([]byte(LiveReloadScript))
}

// liveReloadMiddleware injects the live-reload script tag into HTML responses.
func liveReloadMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only inject into HTML files
		if filepath.Ext(r.URL.Path) != ".html" && r.URL.Path != "/" && filepath.Ext(r.URL.Path) != "" {
			next.ServeHTTP(w, r)
			return
		}

		// Wrap response writer to inject the script tag
		irw := &injectingResponseWriter{
			ResponseWriter: w,
			inject:         []byte(LiveReloadScriptTag),
		}

		next.ServeHTTP(irw, r)

		// Ensure any buffered content is flushed (handles HTML without </html>)
		irw.Flush()
	})
}

// injectingResponseWriter wraps http.ResponseWriter to inject script before </body>.
type injectingResponseWriter struct {
	http.ResponseWriter
	inject    []byte
	injected  bool
	buf       []byte
	committed bool
}

// WriteHeader deletes Content-Length before writing headers.
// Since we inject script bytes, the original Content-Length from http.FileServer
// would be incorrect. Removing it allows Go to use chunked transfer encoding.
func (w *injectingResponseWriter) WriteHeader(code int) {
	w.Header().Del("Content-Length")
	w.ResponseWriter.WriteHeader(code)
}

func (w *injectingResponseWriter) Write(b []byte) (int, error) {
	if w.committed {
		return w.ResponseWriter.Write(b)
	}

	// Buffer the content
	w.buf = append(w.buf, b...)

	// If we've seen </html>, flush everything
	if findLastIndex(w.buf, []byte("</html>")) >= 0 {
		w.injectScript()
		err := w.commit()
		return len(b), err
	}

	return len(b), nil
}

// Flush ensures any remaining buffered content is written.
func (w *injectingResponseWriter) Flush() {
	if !w.committed && len(w.buf) > 0 {
		w.injectScript()
		_ = w.commit()
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *injectingResponseWriter) injectScript() {
	if w.injected {
		return
	}

	insertAt := findLastIndex(w.buf, []byte("</body>"))
	if insertAt < 0 {
		insertAt = findLastIndex(w.buf, []byte("</html>"))
	}
	if insertAt < 0 {
		w.buf = append(w.buf, w.inject...)
	} else {
		newBuf := make([]byte, 0, len(w.buf)+len(w.inject))
		newBuf = append(newBuf, w.buf[:insertAt]...)
		newBuf = append(newBuf, w.inject...)
		newBuf = append(newBuf, w.buf[insertAt:]...)
		w.buf = newBuf
	}
	w.injected = true
}

func (w *injectingResponseWriter) commit() error {
	if w.committed {
		return nil
	}
	w.committed = true
	w.Header().Del("Content-Length")
	_, err := w.ResponseWriter.Write(w.buf)
	return err
}

// findLastIndex finds the last occurrence of needle in haystack.
func findLastIndex(haystack, needle []byte) int {
	if len(needle) == 0 {
		return -1
	}
	return bytes.LastIndex(haystack, needle)
}
