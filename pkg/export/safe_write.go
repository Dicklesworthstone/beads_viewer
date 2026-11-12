package export

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

var (
	// ErrInvalidOutputPath indicates an empty or directory-like output target path.
	ErrInvalidOutputPath = errors.New("invalid export output path")
	// ErrSymlinkOutputRefused indicates the target path is a symbolic link.
	ErrSymlinkOutputRefused = errors.New("refusing to write to symlink output target")
	// ErrDirectoryOutputRefused indicates the target path is an existing directory.
	ErrDirectoryOutputRefused = errors.New("refusing to overwrite directory output target")
)

func hasTrailingPathSeparator(p string) bool {
	if p == "" {
		return false
	}
	last := p[len(p)-1]
	return last == '/' || (filepath.Separator != '/' && last == filepath.Separator)
}

func validateExistingPathNotSymlinkOrDir(rawPath string) error {
	if strings.TrimSpace(rawPath) == "" {
		return fmt.Errorf("%w: empty path", ErrInvalidOutputPath)
	}
	if hasTrailingPathSeparator(rawPath) {
		return fmt.Errorf("%w: %q names a directory", ErrDirectoryOutputRefused, rawPath)
	}
	cleaned := filepath.Clean(rawPath)
	base := filepath.Base(cleaned)
	if base == "" || base == "." || base == ".." || base == string(filepath.Separator) {
		return fmt.Errorf("%w: %q", ErrInvalidOutputPath, rawPath)
	}
	if st, err := os.Lstat(cleaned); err == nil {
		if st.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: %q", ErrSymlinkOutputRefused, rawPath)
		}
		if st.IsDir() {
			return fmt.Errorf("%w: %q", ErrDirectoryOutputRefused, rawPath)
		}
		if !st.Mode().IsRegular() {
			return fmt.Errorf("%w: %q is non-regular (%s)", ErrInvalidOutputPath, rawPath, st.Mode())
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect output path %q: %w", rawPath, err)
	}
	return nil
}

func checkRootDestinationRegularOrMissing(root *os.Root, baseName, displayPath string) (os.FileMode, bool, error) {
	st, err := root.Lstat(baseName)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("inspect output destination %q: %w", displayPath, err)
	}
	if st.Mode()&os.ModeSymlink != 0 {
		return 0, false, fmt.Errorf("%w: %q", ErrSymlinkOutputRefused, displayPath)
	}
	if st.IsDir() {
		return 0, false, fmt.Errorf("%w: %q", ErrDirectoryOutputRefused, displayPath)
	}
	if !st.Mode().IsRegular() {
		return 0, false, fmt.Errorf("%w: %q is non-regular (%s)", ErrInvalidOutputPath, displayPath, st.Mode())
	}
	return st.Mode().Perm(), true, nil
}

// SafeWriteOutput atomically writes data to targetPath inside an os.Root-confined
// parent directory, refusing symlinks, directories, and non-regular files.
func SafeWriteOutput(targetPath string, data []byte, perm os.FileMode) error {
	return SafeWriteOutputFunc(targetPath, perm, func(w io.Writer) error {
		for len(data) > 0 {
			n, err := w.Write(data)
			if err != nil {
				return err
			}
			if n <= 0 {
				return io.ErrShortWrite
			}
			data = data[n:]
		}
		return nil
	})
}

// SafeWriteOutputFunc streams output via writeFn into a unique temporary file in
// targetPath's parent directory (confined via os.OpenRoot), fsyncs and closes it,
// and atomically renames it onto targetPath while refusing symlink or directory targets.
func SafeWriteOutputFunc(targetPath string, perm os.FileMode, writeFn func(io.Writer) error) error {
	if writeFn == nil {
		return errors.New("nil output writer callback")
	}
	if strings.TrimSpace(targetPath) == "" {
		return fmt.Errorf("%w: empty path", ErrInvalidOutputPath)
	}
	if hasTrailingPathSeparator(targetPath) {
		return fmt.Errorf("%w: %q names a directory", ErrDirectoryOutputRefused, targetPath)
	}

	cleaned := filepath.Clean(targetPath)
	baseName := filepath.Base(cleaned)
	if baseName == "" || baseName == "." || baseName == ".." || baseName == string(filepath.Separator) {
		return fmt.Errorf("%w: %q", ErrInvalidOutputPath, targetPath)
	}
	if perm == 0 {
		perm = 0o644
	}

	parentDir := filepath.Dir(cleaned)
	if parentDir == "" {
		parentDir = "."
	}

	// Pre-check before MkdirAll in case targetPath itself already exists as a symlink or directory.
	if err := validateExistingPathNotSymlinkOrDir(cleaned); err != nil {
		return err
	}

	if parentDir != "." {
		if err := os.MkdirAll(parentDir, 0o755); err != nil {
			return fmt.Errorf("create output parent directory for %q: %w", targetPath, err)
		}
	}

	root, err := os.OpenRoot(parentDir)
	if err != nil {
		return fmt.Errorf("open output parent directory for %q: %w", targetPath, err)
	}
	defer root.Close()

	existingPerm, hadExisting, err := checkRootDestinationRegularOrMissing(root, baseName, targetPath)
	if err != nil {
		return err
	}

	prefixBase := baseName
	if len(prefixBase) > 64 {
		prefixBase = prefixBase[:64]
	}
	tmpName := fmt.Sprintf(".bv-out-%s-%s.tmp", prefixBase, rand.Text())

	effectivePerm := perm
	if hadExisting {
		effectivePerm = existingPerm
	}

	tmpFile, err := root.OpenFile(tmpName, os.O_RDWR|os.O_CREATE|os.O_EXCL, effectivePerm)
	if err != nil {
		return fmt.Errorf("create temporary output file for %q: %w", targetPath, err)
	}

	committed := false
	defer func() {
		if !committed {
			_ = root.Remove(tmpName)
		}
	}()

	if err := writeFn(tmpFile); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if hadExisting {
		if err := tmpFile.Chmod(effectivePerm); err != nil {
			_ = tmpFile.Close()
			return fmt.Errorf("preserve output file mode for %q: %w", targetPath, err)
		}
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("sync temporary output file for %q: %w", targetPath, err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("close temporary output file for %q: %w", targetPath, err)
	}

	// Re-verify destination immediately prior to atomic rename inside os.Root.
	if _, _, err := checkRootDestinationRegularOrMissing(root, baseName, targetPath); err != nil {
		return err
	}

	if err := root.Rename(tmpName, baseName); err != nil {
		return fmt.Errorf("commit output file %q: %w", targetPath, err)
	}
	committed = true
	return nil
}
