// Package export provides data export functionality for bv.
//
// This file implements a local preview server for static site bundles.
// It serves files with no-cache headers and auto-opens the browser.
package export

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	iofs "io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// PreviewServer serves a static site bundle locally for previewing.
type PreviewServer struct {
	bundlePath string
	port       int

	// SAFETY: server publication and stopRequested form one lifecycle state.
	// Never call into net/http while holding mu: Shutdown may wait for handlers,
	// and handlers are allowed to call PreviewServer methods.
	mu            sync.Mutex
	server        *http.Server
	stopRequested bool

	liveReloadHub *LiveReloadHub
}

// NewPreviewServer creates a new preview server for the given bundle.
func NewPreviewServer(bundlePath string, port int) *PreviewServer {
	return &PreviewServer{
		bundlePath: bundlePath,
		port:       port,
	}
}

// Start starts the preview server and blocks until stopped.
func (p *PreviewServer) Start() error {
	return p.start(PreviewConfig{OpenBrowser: true})
}

// StartWithGracefulShutdown starts the server with signal handling for clean shutdown.
func (p *PreviewServer) StartWithGracefulShutdown() error {
	return p.startWithGracefulShutdown(PreviewConfig{OpenBrowser: true})
}

func (p *PreviewServer) startWithGracefulShutdown(config PreviewConfig) error {
	// Channel to receive OS signals
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(stop)

	// Every server exit, including a graceful external Stop, must wake the
	// owner. Suppressing http.ErrServerClosed here previously left the select
	// below blocked forever after another goroutine called Stop.
	serverDone := make(chan error, 1)

	// Start server in goroutine
	go func() {
		serverDone <- p.start(config)
	}()

	// Wait for either signal or server completion.
	select {
	case <-stop:
		if !config.Quiet {
			fmt.Println("\nShutting down preview server...")
		}
		stopErr := p.Stop()
		// Stop force-closes the listener if graceful shutdown times out, so the
		// owner can always join the server goroutine before returning.
		serverErr := <-serverDone
		return errors.Join(stopErr, serverErr)
	case err := <-serverDone:
		return err
	}
}

// Stop gracefully stops the preview server.
func (p *PreviewServer) Stop() error {
	server, hub := p.requestStop()
	if hub != nil {
		hub.Stop()
	}
	if server == nil {
		return nil
	}
	return shutdownPreviewServer(server)
}

func shutdownPreviewServer(server *http.Server) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		shutdownErr := fmt.Errorf("graceful preview shutdown: %w", err)
		// Shutdown leaves the server running when its context expires. Close is
		// the bounded fallback that preserves the owner/child join contract.
		if closeErr := server.Close(); closeErr != nil && !errors.Is(closeErr, http.ErrServerClosed) {
			return errors.Join(shutdownErr, fmt.Errorf("force-close preview server: %w", closeErr))
		}
		return shutdownErr
	}
	return nil
}

func (p *PreviewServer) publishServer(server *http.Server, port int, hub *LiveReloadHub) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.stopRequested {
		return http.ErrServerClosed
	}
	if p.server != nil {
		return fmt.Errorf("preview server already started")
	}
	p.server = server
	p.port = port
	p.liveReloadHub = hub
	return nil
}

func (p *PreviewServer) requestStop() (*http.Server, *LiveReloadHub) {
	p.mu.Lock()
	p.stopRequested = true
	server := p.server
	hub := p.liveReloadHub
	p.mu.Unlock()
	return server, hub
}

// Port returns the port the server is running on.
func (p *PreviewServer) Port() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.port
}

// URL returns the full URL of the preview server.
func (p *PreviewServer) URL() string {
	return fmt.Sprintf("http://127.0.0.1:%d", p.Port())
}

// statusHandler returns the preview server status as JSON.
func (p *PreviewServer) statusHandler(previewFS *safePreviewDir) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")

		hasIndex := previewFS != nil && validatePreviewIndex(previewFS, p.bundlePath) == nil

		// Get bundle info
		var fileCount int
		if previewFS != nil {
			_ = iofs.WalkDir(previewFS.root.FS(), ".", func(_ string, entry iofs.DirEntry, err error) error {
				if err == nil && !entry.IsDir() {
					fileCount++
				}
				return nil
			})
		}

		type statusResponse struct {
			Status     string `json:"status"`
			Port       int    `json:"port"`
			BundlePath string `json:"bundle_path"`
			HasIndex   bool   `json:"has_index"`
			FileCount  int    `json:"file_count"`
		}

		resp := statusResponse{
			Status:     "running",
			Port:       p.Port(),
			BundlePath: strings.ToValidUTF8(p.bundlePath, "?"),
			HasIndex:   hasIndex,
			FileCount:  fileCount,
		}

		if err := json.NewEncoder(w).Encode(resp); err != nil {
			http.Error(w, fmt.Sprintf("encode preview status: %v", err), http.StatusInternalServerError)
			return
		}
	}
}

// noCacheMiddleware adds headers to prevent browser caching.
func noCacheMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Set no-cache headers
		w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Expires", "0")

		next.ServeHTTP(w, r)
	})
}

// previewRequestGuard binds browser access to the listener's local origin.
// Loopback binding alone does not reject a hostile Host after DNS rebinding.
// Apply this outside the mux so data, status and live-reload share the policy.
func previewRequestGuard(port int, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		authority, allowed := previewAuthority(r.Host, port)
		origins := r.Header.Values("Origin")
		if allowed && len(origins) != 0 {
			allowed = len(origins) == 1
			if allowed {
				origin, err := url.Parse(origins[0])
				allowed = err == nil && origin.Scheme == "http" && origin.User == nil && origin.Opaque == "" &&
					origin.Path == "" && origin.RawQuery == "" && !origin.ForceQuery && !strings.Contains(origins[0], "#")
				if allowed {
					originAuthority, local := previewAuthority(origin.Host, port)
					allowed = local && originAuthority == authority
				}
			}
		}
		if !allowed {
			w.Header().Set("Cache-Control", "no-store")
			http.Error(w, "preview requests must use the local preview origin", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func previewAuthority(authority string, port int) (string, bool) {
	host, rawPort, err := net.SplitHostPort(authority)
	if err != nil {
		// An omitted port means HTTP's default, not the preview's chosen port.
		if strings.ContainsAny(authority, ":[]") {
			return "", false
		}
		host, rawPort = authority, "80"
	}
	for _, digit := range rawPort {
		if digit < '0' || digit > '9' {
			return "", false
		}
	}
	parsedPort, err := strconv.Atoi(rawPort)
	if err != nil || parsedPort != port || port <= 0 || port > 65535 {
		return "", false
	}
	host = strings.ToLower(host)
	if host != "localhost" {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return "", false
		}
		host = ip.String()
	}
	return net.JoinHostPort(host, strconv.Itoa(port)), true
}

type safePreviewDir struct {
	root *os.Root
}

func newSafePreviewFileSystem(bundlePath string) (*safePreviewDir, error) {
	root, err := os.OpenRoot(bundlePath)
	if err != nil {
		return nil, fmt.Errorf("open preview directory: %w", err)
	}
	return &safePreviewDir{root: root}, nil
}

func (d *safePreviewDir) Close() error {
	return d.root.Close()
}

func validatePreviewBundle(bundlePath string) (*safePreviewDir, error) {
	if bundlePath == "" {
		return nil, fmt.Errorf("bundle path is required")
	}
	bundleInfo, err := os.Stat(bundlePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("bundle path does not exist: %s", bundlePath)
		}
		return nil, fmt.Errorf("stat bundle path %s: %w", bundlePath, err)
	}
	if !bundleInfo.IsDir() {
		return nil, fmt.Errorf("bundle path is not a directory: %s", bundlePath)
	}

	previewFS, err := newSafePreviewFileSystem(bundlePath)
	if err != nil {
		return nil, err
	}
	if err := validatePreviewIndex(previewFS, bundlePath); err != nil {
		previewFS.Close()
		return nil, err
	}

	return previewFS, nil
}

func validatePreviewIndex(previewFS http.FileSystem, bundlePath string) error {
	indexPath := filepath.Join(bundlePath, "index.html")
	indexFile, err := previewFS.Open("/index.html")
	if err != nil {
		if errors.Is(err, iofs.ErrPermission) {
			return fmt.Errorf("index.html escapes bundle: %s", indexPath)
		}
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("no index.html found in bundle: %s", bundlePath)
		}
		return fmt.Errorf("open bundle index %s: %w", indexPath, err)
	}

	indexInfo, statErr := indexFile.Stat()
	closeErr := indexFile.Close()
	if statErr != nil {
		return fmt.Errorf("stat bundle index %s: %w", indexPath, statErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close bundle index %s: %w", indexPath, closeErr)
	}
	if indexInfo.IsDir() {
		return fmt.Errorf("index.html is a directory in bundle: %s", indexPath)
	}

	return nil
}

func (d *safePreviewDir) Open(name string) (http.File, error) {
	rel := strings.TrimPrefix(path.Clean("/"+name), "/")
	if rel == "" {
		rel = "."
	}
	if !iofs.ValidPath(rel) {
		return nil, iofs.ErrPermission
	}
	localRel, err := filepath.Localize(rel)
	if err != nil {
		return nil, iofs.ErrPermission
	}

	file, err := d.root.Open(localRel)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		// Root's path-escape errors intentionally have no public sentinel.
		// Treat every other failed open as forbidden, without exposing details
		// about the filesystem beyond the selected directory.
		return nil, &os.PathError{Op: "open", Path: name, Err: iofs.ErrPermission}
	}
	return file, nil
}

// FindAvailablePort finds an available port in the given range.
func FindAvailablePort(start, end int) (int, error) {
	for port := start; port <= end; port++ {
		listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			listener.Close()
			return port, nil
		}
	}
	return 0, fmt.Errorf("no available port in range %d-%d", start, end)
}

// DefaultPreviewPort is the default port for the preview server.
const DefaultPreviewPort = 9000

// PreviewPortRange defines the range of ports to try if default is unavailable.
const PreviewPortRangeStart = 9000
const PreviewPortRangeEnd = 9100

// StartPreview is a convenience function to start a preview server with auto port selection.
func StartPreview(bundlePath string) error {
	port, err := FindAvailablePort(PreviewPortRangeStart, PreviewPortRangeEnd)
	if err != nil {
		return fmt.Errorf("could not find available port: %w", err)
	}

	server := NewPreviewServer(bundlePath, port)
	return server.StartWithGracefulShutdown()
}

// PreviewConfig configures the preview server.
type PreviewConfig struct {
	// BundlePath is the path to the static site bundle
	BundlePath string

	// Port is the port to serve on (0 for auto-select)
	Port int

	// OpenBrowser determines whether to auto-open a browser
	OpenBrowser bool

	// Quiet suppresses status messages
	Quiet bool

	// LiveReload enables live-reload via SSE when bundle files change
	LiveReload bool
}

// DefaultPreviewConfig returns sensible defaults for preview configuration.
func DefaultPreviewConfig() PreviewConfig {
	return PreviewConfig{
		Port:        0, // Auto-select
		OpenBrowser: true,
		Quiet:       false,
		LiveReload:  true, // Enable by default for immediate feedback
	}
}

// StartPreviewWithConfig starts a preview server with the given configuration.
func StartPreviewWithConfig(config PreviewConfig) error {
	// Auto-select port if needed
	port := config.Port
	if port == 0 {
		var err error
		port, err = FindAvailablePort(PreviewPortRangeStart, PreviewPortRangeEnd)
		if err != nil {
			return fmt.Errorf("could not find available port: %w", err)
		}
	}

	server := NewPreviewServer(config.BundlePath, port)
	return server.startWithGracefulShutdown(config)
}

// start owns the selected root until all HTTP handlers have stopped. Both
// public preview entry points use this same filesystem and request boundary.
func (p *PreviewServer) start(config PreviewConfig) error {
	previewFS, err := validatePreviewBundle(p.bundlePath)
	if err != nil {
		return err
	}
	defer previewFS.Close()

	port := p.Port()
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}
	defer listener.Close()
	port = listener.Addr().(*net.TCPAddr).Port

	// Need to initialize the server first
	mux := http.NewServeMux()
	fileServer := http.FileServer(previewFS)

	// Set up live-reload if enabled
	var liveReloadHub *LiveReloadHub
	if config.LiveReload {
		var err error
		liveReloadHub, err = NewLiveReloadHub(p.bundlePath)
		if err != nil {
			if !config.Quiet {
				fmt.Printf("Warning: Could not enable live-reload: %v\n", err)
			}
		} else {
			if err := liveReloadHub.Start(); err != nil {
				if !config.Quiet {
					fmt.Printf("Warning: Could not start live-reload: %v\n", err)
				}
				liveReloadHub.Stop()
				liveReloadHub = nil
			} else {
				defer liveReloadHub.Stop()
				// Add SSE endpoint for live-reload and the script that subscribes
				// to it (served as a file so the dashboard's CSP, which forbids
				// inline scripts, still lets it run).
				mux.HandleFunc("/__preview__/events", liveReloadHub.SSEHandler())
				mux.HandleFunc(LiveReloadScriptPath, liveReloadScriptHandler)
				// Wrap file server with live-reload script-tag injection
				mux.Handle("/", liveReloadMiddleware(noCacheMiddleware(fileServer)))
			}
		}
	}

	// If no live-reload, just use no-cache middleware
	if liveReloadHub == nil {
		mux.Handle("/", noCacheMiddleware(fileServer))
	}

	mux.HandleFunc("/__preview__/status", p.statusHandler(previewFS))

	httpServer := &http.Server{
		Addr:              listener.Addr().String(),
		Handler:           previewRequestGuard(port, mux),
		ReadTimeout:       10 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	if err := p.publishServer(httpServer, port, liveReloadHub); err != nil {
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}

	// Handle opening browser. Resolve the command and environment NOW; the
	// delayed goroutine must not consult PATH/env later, when a different
	// caller (or the next test) may have changed them.
	if config.OpenBrowser {
		url := p.URL()
		opener, err := prepareBrowserOpen(url)
		if err != nil {
			if !config.Quiet {
				fmt.Printf("Could not open browser: %v\n", err)
				fmt.Printf("Open %s in your browser\n", url)
			}
		} else if opener != nil {
			go func() {
				time.Sleep(500 * time.Millisecond)
				if err := opener.Start(); err != nil {
					if !config.Quiet {
						fmt.Printf("Could not open browser: %v\n", err)
						fmt.Printf("Open %s in your browser\n", url)
					}
				}
			}()
		}
	}

	// Print status message
	if !config.Quiet {
		fmt.Printf("\nPreview server running at http://127.0.0.1:%d\n", port)
		fmt.Printf("Serving: %s\n", p.bundlePath)
		if liveReloadHub != nil {
			fmt.Println("Live-reload: enabled (browser will refresh on file changes)")
		}
		fmt.Println("\nPress Ctrl+C to stop")
	}

	serveErr := httpServer.Serve(listener)
	if errors.Is(serveErr, http.ErrServerClosed) {
		serveErr = nil
	}
	if liveReloadHub != nil {
		liveReloadHub.Stop()
	}
	// Serve returns as soon as Shutdown closes the listener, before active
	// handlers finish. Join that drain before releasing their directory root.
	return errors.Join(serveErr, shutdownPreviewServer(httpServer))
}
