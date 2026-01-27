// Package export provides data export functionality for bv.
//
// This file implements live-reload via Server-Sent Events (SSE) for the preview server.
// When files change in the bundle directory, connected browsers receive reload events.
package export

import (
	"context"
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

	// context for shutdown
	ctx    context.Context
	cancel context.CancelFunc

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

	ctx, cancel := context.WithCancel(context.Background())

	hub := &LiveReloadHub{
		bundlePath: bundlePath,
		watcher:    watcher,
		clients:    make(map[chan struct{}]struct{}),
		ctx:        ctx,
		cancel:     cancel,
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
	h.cancel()
	h.watcher.Close()

	// Close all client channels
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.clients {
		close(ch)
	}
	h.clients = make(map[chan struct{}]struct{})
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
		case <-h.ctx.Done():
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
		// Set SSE headers
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("Access-Control-Allow-Origin", "*")

		// Ensure we can flush
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "SSE not supported", http.StatusInternalServerError)
			return
		}

		// Register this client
		clientCh := make(chan struct{}, 1)
		h.mu.Lock()
		h.clients[clientCh] = struct{}{}
		h.mu.Unlock()

		// Cleanup on disconnect
		defer func() {
			h.mu.Lock()
			delete(h.clients, clientCh)
			h.mu.Unlock()
		}()

		// Send initial connection event
		fmt.Fprintf(w, "event: connected\ndata: {\"status\":\"connected\"}\n\n")
		flusher.Flush()

		// Keep connection open and send events
		for {
			select {
			case <-r.Context().Done():
				return
			case <-h.ctx.Done():
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

// LiveReloadScript returns JavaScript that connects to the SSE endpoint and reloads on events.
const LiveReloadScript = `<script>
(function() {
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
</script>`

// liveReloadMiddleware injects the live-reload script into HTML responses.
func liveReloadMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only inject into HTML files
		if filepath.Ext(r.URL.Path) != ".html" && r.URL.Path != "/" && filepath.Ext(r.URL.Path) != "" {
			next.ServeHTTP(w, r)
			return
		}

		// Wrap response writer to inject script
		irw := &injectingResponseWriter{
			ResponseWriter: w,
			inject:         []byte(LiveReloadScript),
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

func (w *injectingResponseWriter) WriteHeader(code int) {
	// Remove Content-Length because we will inject extra bytes,
	// making the original length incorrect.
	w.Header().Del("Content-Length")
	w.ResponseWriter.WriteHeader(code)
}

func (w *injectingResponseWriter) Write(b []byte) (int, error) {
	if w.committed {
		return w.ResponseWriter.Write(b)
	}

	// Buffer the content
	w.buf = append(w.buf, b...)

	// Check if we have the closing body tag
	bodyClose := []byte("</body>")
	if idx := findLastIndex(w.buf, bodyClose); idx >= 0 && !w.injected {
		// Inject before </body>
		newBuf := make([]byte, 0, len(w.buf)+len(w.inject))
		newBuf = append(newBuf, w.buf[:idx]...)
		newBuf = append(newBuf, w.inject...)
		newBuf = append(newBuf, w.buf[idx:]...)
		w.buf = newBuf
		w.injected = true
	}

	// If we've seen </html>, flush everything
	if findLastIndex(w.buf, []byte("</html>")) >= 0 {
		w.committed = true
		_, err := w.ResponseWriter.Write(w.buf)
		return len(b), err
	}

	return len(b), nil
}

// Flush ensures any remaining buffered content is written.
func (w *injectingResponseWriter) Flush() {
	if !w.committed && len(w.buf) > 0 {
		w.committed = true
		// Inject at end if we haven't yet and there's content
		if !w.injected {
			w.buf = append(w.buf, w.inject...)
		}
		w.ResponseWriter.Write(w.buf)
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// findLastIndex finds the last occurrence of needle in haystack.
func findLastIndex(haystack, needle []byte) int {
	if len(needle) == 0 {
		return -1
	}
	for i := len(haystack) - len(needle); i >= 0; i-- {
		found := true
		for j := 0; j < len(needle); j++ {
			if haystack[i+j] != needle[j] {
				found = false
				break
			}
		}
		if found {
			return i
		}
	}
	return -1
}
