package export

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestPreviewServer_StartWithGracefulShutdown_ReturnsStartError(t *testing.T) {
	server := NewPreviewServer("/path/does/not/exist", 9010)
	if err := server.StartWithGracefulShutdown(); err == nil {
		t.Fatal("Expected StartWithGracefulShutdown to return error for missing bundle path")
	}
}

func TestPreviewServerReleasesRootOnFailedOrCanceledStart(t *testing.T) {
	for _, mode := range []string{"invalid index", "occupied port", "already stopped"} {
		t.Run(mode, func(t *testing.T) {
			bundle := t.TempDir()
			port := 0
			if mode != "invalid index" {
				if err := os.WriteFile(filepath.Join(bundle, "index.html"), []byte("<html>fixture</html>"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "occupied port" {
				listener, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				defer listener.Close()
				port = listener.Addr().(*net.TCPAddr).Port
			}
			server := NewPreviewServer(bundle, port)
			if mode == "already stopped" {
				if err := server.Stop(); err != nil {
					t.Fatal(err)
				}
			}
			err := server.StartWithGracefulShutdown()
			if (err == nil) != (mode == "already stopped") {
				t.Fatalf("unexpected startup outcome: %v", err)
			}
			// On Windows, leaking the directory handle would prevent this move.
			if err := os.Rename(bundle, bundle+"-released"); err != nil {
				t.Fatalf("failed startup retained its preview root: %v", err)
			}
		})
	}
}

func TestPreviewServer_StartWithGracefulShutdown_ReturnsAfterExternalStop(t *testing.T) {
	bundleDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(bundleDir, "index.html"), []byte("<!doctype html><title>ok</title>"), 0644); err != nil {
		t.Fatalf("WriteFile index.html: %v", err)
	}

	port, err := FindAvailablePort(19091, 19110)
	if err != nil {
		t.Fatalf("FindAvailablePort: %v", err)
	}
	server := NewPreviewServer(bundleDir, port)
	t.Cleanup(func() { _ = server.Stop() })
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- server.StartWithGracefulShutdown()
	}()

	client := &http.Client{Timeout: 100 * time.Millisecond}
	startupDeadline := time.NewTimer(2 * time.Second)
	defer startupDeadline.Stop()
	startupPoll := time.NewTicker(10 * time.Millisecond)
	defer startupPoll.Stop()
	for {
		response, requestErr := client.Get(server.URL() + "/__preview__/status")
		if requestErr == nil {
			_ = response.Body.Close()
			break
		}

		select {
		case err := <-serverDone:
			t.Fatalf("preview server exited before startup completed: %v", err)
		case <-startupDeadline.C:
			t.Fatal("preview server did not start before timeout")
		case <-startupPoll.C:
		}
	}

	if err := server.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	shutdownDeadline := time.NewTimer(time.Second)
	defer shutdownDeadline.Stop()
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatalf("StartWithGracefulShutdown returned error after Stop: %v", err)
		}
	case <-shutdownDeadline.C:
		t.Fatal("StartWithGracefulShutdown remained blocked after external Stop")
	}
}

func TestStartPreview_ReturnsBundleError(t *testing.T) {
	if err := StartPreview("/path/does/not/exist"); err == nil {
		t.Fatal("Expected StartPreview to return error for missing bundle path")
	}
}

func TestStartPreviewWithConfig_PortInUseReturnsError(t *testing.T) {
	bundleDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(bundleDir, "index.html"), []byte("<!doctype html><title>ok</title>"), 0644); err != nil {
		t.Fatalf("WriteFile index.html: %v", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	port := listener.Addr().(*net.TCPAddr).Port
	cfg := PreviewConfig{
		BundlePath:  bundleDir,
		Port:        port,
		OpenBrowser: false,
		Quiet:       true,
	}

	if err := StartPreviewWithConfig(cfg); err == nil {
		t.Fatal("Expected StartPreviewWithConfig to return error when port is already in use")
	}
}

func TestStartPreviewWithConfig_PortInUseDoesNotOpenBrowser(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script stubs not supported on windows in this test")
	}

	browserCommand, _, err := browserOpenCommandForGOOS(runtime.GOOS, "http://127.0.0.1:1")
	if err != nil {
		t.Skipf("browser open command is unsupported on this platform: %v", err)
	}

	bundleDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(bundleDir, "index.html"), []byte("<!doctype html><title>ok</title>"), 0644); err != nil {
		t.Fatalf("WriteFile index.html: %v", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	binDir := t.TempDir()
	browserLog := filepath.Join(binDir, "browser.log")
	browserScript := `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$BV_BROWSER_LOG"
`
	writeExecutable(t, binDir, browserCommand, browserScript)

	t.Setenv("PATH", binDir)
	t.Setenv("BV_BROWSER_LOG", browserLog)
	t.Setenv("BV_NO_BROWSER", "")
	t.Setenv("BV_TEST_MODE", "")

	cfg := PreviewConfig{
		BundlePath:  bundleDir,
		Port:        listener.Addr().(*net.TCPAddr).Port,
		OpenBrowser: true,
		Quiet:       true,
	}

	if err := StartPreviewWithConfig(cfg); err == nil {
		t.Fatal("Expected StartPreviewWithConfig to return error when port is already in use")
	}

	time.Sleep(700 * time.Millisecond)

	content, err := os.ReadFile(browserLog)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		t.Fatalf("ReadFile browser log: %v", err)
	}
	if strings.TrimSpace(string(content)) != "" {
		t.Fatalf("browser opener ran even though preview server failed to bind: %q", string(content))
	}
}
