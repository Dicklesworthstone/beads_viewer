package export

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

func assertNoSafeWriteTempFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir %s: %v", dir, err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".bv-out-") || strings.HasSuffix(name, ".tmp") {
			t.Fatalf("unexpected leftover temp file %q in %s", name, dir)
		}
	}
}

func TestSafeWriteOutput_CreateAndAtomicOverwrite(t *testing.T) {
	baseDir := t.TempDir()
	target := filepath.Join(baseDir, "nested", "subdir", "report.json")

	firstPayload := []byte("{\"version\":1}\n")
	if err := SafeWriteOutput(target, firstPayload, 0o600); err != nil {
		t.Fatalf("initial SafeWriteOutput failed: %v", err)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read written target: %v", err)
	}
	if !bytes.Equal(got, firstPayload) {
		t.Fatalf("got %q, want %q", got, firstPayload)
	}
	firstInfo, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if mode := firstInfo.Mode().Perm(); mode != 0o600 {
		t.Fatalf("initial file mode = %o, want 0600", mode)
	}

	secondPayload := []byte("{\"version\":2,\"status\":\"ok\"}\n")
	if err := SafeWriteOutput(target, secondPayload, 0o644); err != nil {
		t.Fatalf("overwrite SafeWriteOutput failed: %v", err)
	}

	gotAfter, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read overwritten target: %v", err)
	}
	if !bytes.Equal(gotAfter, secondPayload) {
		t.Fatalf("got %q, want %q", gotAfter, secondPayload)
	}
	secondInfo, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(firstInfo, secondInfo) {
		t.Fatalf("expected atomic rename replacement with new inode, but inode did not change")
	}
	if mode := secondInfo.Mode().Perm(); mode != 0o600 {
		t.Fatalf("overwrite widened existing private file permissions: got %o, want 0600", mode)
	}
	assertNoSafeWriteTempFiles(t, filepath.Dir(target))
}

func TestSafeWriteOutput_RefusesSymlinkTarget(t *testing.T) {
	dir := t.TempDir()
	victimPath := filepath.Join(dir, "victim.txt")
	original := []byte("sensitive victim content\n")
	if err := os.WriteFile(victimPath, original, 0o600); err != nil {
		t.Fatal(err)
	}

	linkPath := filepath.Join(dir, "output-link.json")
	if err := os.Symlink(victimPath, linkPath); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}

	err := SafeWriteOutput(linkPath, []byte("clobbered"), 0o644)
	if !errors.Is(err, ErrSymlinkOutputRefused) {
		t.Fatalf("SafeWriteOutput on symlink err = %v, want ErrSymlinkOutputRefused", err)
	}

	victimBytes, readErr := os.ReadFile(victimPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(victimBytes, original) {
		t.Fatalf("symlink target was modified: got %q, want %q", victimBytes, original)
	}

	// Dangling symlinks must also fail closed without creating the target.
	missingTarget := filepath.Join(dir, "should-not-exist.txt")
	danglingLink := filepath.Join(dir, "dangling.json")
	if err := os.Symlink(missingTarget, danglingLink); err != nil {
		t.Fatal(err)
	}
	if err := SafeWriteOutput(danglingLink, []byte("clobbered"), 0o644); !errors.Is(err, ErrSymlinkOutputRefused) {
		t.Fatalf("SafeWriteOutput on dangling symlink err = %v, want ErrSymlinkOutputRefused", err)
	}
	if _, statErr := os.Lstat(missingTarget); !os.IsNotExist(statErr) {
		t.Fatalf("dangling symlink target was unexpectedly created: %v", statErr)
	}
	assertNoSafeWriteTempFiles(t, dir)
}

func TestSafeWriteOutput_RefusesDirectoryAndInvalidPaths(t *testing.T) {
	dir := t.TempDir()
	existingSubdir := filepath.Join(dir, "existing-dir")
	if err := os.MkdirAll(existingSubdir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := SafeWriteOutput(existingSubdir, []byte("data"), 0o644); !errors.Is(err, ErrDirectoryOutputRefused) {
		t.Fatalf("SafeWriteOutput on existing directory err = %v, want ErrDirectoryOutputRefused", err)
	}
	if err := SafeWriteOutput(existingSubdir+string(filepath.Separator), []byte("data"), 0o644); !errors.Is(err, ErrDirectoryOutputRefused) {
		t.Fatalf("SafeWriteOutput on trailing slash err = %v, want ErrDirectoryOutputRefused", err)
	}
	for _, invalid := range []string{"", "   ", ".", ".."} {
		if err := SafeWriteOutput(invalid, []byte("data"), 0o644); !errors.Is(err, ErrInvalidOutputPath) {
			t.Fatalf("SafeWriteOutput(%q) err = %v, want ErrInvalidOutputPath", invalid, err)
		}
	}
	assertNoSafeWriteTempFiles(t, dir)
}

func TestSafeWriteOutputFunc_CleansUpTempFileOnWriteFailure(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "existing.txt")
	original := []byte("keep-intact")
	if err := os.WriteFile(target, original, 0o644); err != nil {
		t.Fatal(err)
	}

	boom := errors.New("simulated encoder failure")
	err := SafeWriteOutputFunc(target, 0o644, func(w io.Writer) error {
		_, _ = w.Write([]byte("partial-garbage"))
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("SafeWriteOutputFunc err = %v, want %v", err, boom)
	}

	got, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("failed write corrupted existing file: got %q, want %q", got, original)
	}
	assertNoSafeWriteTempFiles(t, dir)
}

func TestSafeWriteOutput_ConcurrentWritesRaceSafe(t *testing.T) {
	dir := t.TempDir()
	sharedTarget := filepath.Join(dir, "concurrent.json")

	const workers = 16
	validPayloads := make(map[string]bool, workers)
	payloads := make([][]byte, workers)
	for i := 0; i < workers; i++ {
		p := fmt.Sprintf("{\"worker\":%d,\"payload\":%q}\n", i, strings.Repeat("x", 512))
		payloads[i] = []byte(p)
		validPayloads[p] = true
	}

	var wg sync.WaitGroup
	errs := make(chan error, workers*2)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			if err := SafeWriteOutput(sharedTarget, payloads[idx], 0o644); err != nil {
				errs <- err
			}
			distinct := filepath.Join(dir, fmt.Sprintf("worker-%d.json", idx))
			if err := SafeWriteOutput(distinct, payloads[idx], 0o644); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Fatalf("concurrent SafeWriteOutput error: %v", err)
	}

	finalBytes, err := os.ReadFile(sharedTarget)
	if err != nil {
		t.Fatal(err)
	}
	if !validPayloads[string(finalBytes)] {
		t.Fatalf("shared target contains torn/unexpected content (%d bytes)", len(finalBytes))
	}
	assertNoSafeWriteTempFiles(t, dir)
}

func TestGraphExporters_RefuseSymlinkTargets(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim.txt")
	original := []byte("do-not-clobber")
	if err := os.WriteFile(victim, original, 0o600); err != nil {
		t.Fatal(err)
	}

	issues := []model.Issue{
		{ID: "bv-1", Title: "First issue", Status: model.StatusOpen, Priority: 1},
	}
	analyzer := analysis.NewAnalyzer(issues)
	stats := analyzer.Analyze()

	htmlLink := filepath.Join(dir, "graph.html")
	svgLink := filepath.Join(dir, "graph.svg")
	pngLink := filepath.Join(dir, "graph.png")
	for _, link := range []string{htmlLink, svgLink, pngLink} {
		if err := os.Symlink(victim, link); err != nil {
			t.Skipf("symlinks unsupported: %v", err)
		}
	}

	if _, err := GenerateInteractiveGraphHTML(InteractiveGraphOptions{
		Issues: issues,
		Stats:  &stats,
		Path:   htmlLink,
	}); !errors.Is(err, ErrSymlinkOutputRefused) {
		t.Fatalf("GenerateInteractiveGraphHTML on symlink err = %v, want ErrSymlinkOutputRefused", err)
	}

	if err := SaveGraphSnapshot(GraphSnapshotOptions{
		Issues: issues,
		Stats:  &stats,
		Path:   svgLink,
		Format: "svg",
	}); !errors.Is(err, ErrSymlinkOutputRefused) {
		t.Fatalf("SaveGraphSnapshot(svg) on symlink err = %v, want ErrSymlinkOutputRefused", err)
	}

	if err := SaveGraphSnapshot(GraphSnapshotOptions{
		Issues: issues,
		Stats:  &stats,
		Path:   pngLink,
		Format: "png",
	}); !errors.Is(err, ErrSymlinkOutputRefused) {
		t.Fatalf("SaveGraphSnapshot(png) on symlink err = %v, want ErrSymlinkOutputRefused", err)
	}

	gotVictim, err := os.ReadFile(victim)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotVictim, original) {
		t.Fatalf("victim file modified via exporter symlink: got %q, want %q", gotVictim, original)
	}
}
