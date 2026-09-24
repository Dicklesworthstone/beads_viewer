//go:build (darwin || linux) && !android

package agents

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

// agentFilePathInfo returns lstat semantics with an identity that os.SameFile
// can compare to an open descriptor. Unix FileInfo already carries device and
// inode data, so the ordinary Lstat result is sufficient.
func agentFilePathInfo(path string) (os.FileInfo, error) {
	return os.Lstat(path)
}

func openAgentFileForInspection(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

func openAndLockAgentFileForMutation(path string, timeout time.Duration) (*os.File, func() error, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	deadline := time.Now().Add(timeout)
	for {
		err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return file, func() error {
				return unix.Flock(int(file.Fd()), unix.LOCK_UN)
			}, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EINTR) {
			_ = file.Close()
			return nil, nil, err
		}
		if time.Now().After(deadline) {
			_ = file.Close()
			return nil, nil, fmt.Errorf("%w after %s", errAgentFileBusy, timeout)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func prepareAgentReplacementMetadata(source, replacement *os.File, mode os.FileMode) error {
	var sourceStat unix.Stat_t
	if err := unix.Fstat(int(source.Fd()), &sourceStat); err != nil {
		return fmt.Errorf("stat source metadata: %w", err)
	}
	if sourceStat.Nlink != 1 {
		return fmt.Errorf("refusing to replace agent file with %d hard links", sourceStat.Nlink)
	}

	var replacementStat unix.Stat_t
	if err := unix.Fstat(int(replacement.Fd()), &replacementStat); err != nil {
		return fmt.Errorf("stat replacement metadata: %w", err)
	}
	if replacementStat.Uid != sourceStat.Uid || replacementStat.Gid != sourceStat.Gid {
		if err := unix.Fchown(int(replacement.Fd()), int(sourceStat.Uid), int(sourceStat.Gid)); err != nil {
			return fmt.Errorf("preserve owner/group: %w", err)
		}
	}
	// Chown can clear set-ID bits, so apply the complete source mode afterward.
	if err := replacement.Chmod(mode); err != nil {
		return fmt.Errorf("preserve mode: %w", err)
	}
	if err := copyAgentExtendedAttributes(source, replacement); err != nil {
		return err
	}
	if err := copyAgentPlatformFileFlags(source, replacement); err != nil {
		return err
	}
	return nil
}

func copyAgentExtendedAttributes(source, replacement *os.File) error {
	sourceNames, supported, err := agentExtendedAttributeNames(source)
	if err != nil {
		return fmt.Errorf("list source extended attributes: %w", err)
	}
	if !supported {
		return nil
	}
	replacementNames, replacementSupported, err := agentExtendedAttributeNames(replacement)
	if err != nil {
		return fmt.Errorf("list replacement extended attributes: %w", err)
	}
	if !replacementSupported {
		return fmt.Errorf("replacement filesystem stopped supporting extended attributes")
	}

	sourceNameSet := make(map[string]struct{}, len(sourceNames))
	for _, name := range sourceNames {
		if agentExtendedAttributeRequiresRecomputation(name) {
			return fmt.Errorf("refusing to replace agent file with content-bound extended attribute %q", name)
		}
		sourceNameSet[name] = struct{}{}
	}
	// Temp creation (and Darwin cloning) can apply metadata inherited from the
	// current parent directory that the older source never had. Remove every
	// replacement-only xattr before copying source values so an atomic save does
	// not silently broaden a POSIX ACL or retain another automatic attribute.
	for _, name := range replacementNames {
		if _, exists := sourceNameSet[name]; exists {
			continue
		}
		if err := unix.Fremovexattr(int(replacement.Fd()), name); err != nil {
			return fmt.Errorf("remove replacement-only extended attribute %q: %w", name, err)
		}
	}

	for _, name := range sourceNames {
		valueSize, err := unix.Fgetxattr(int(source.Fd()), name, nil)
		if err != nil {
			return fmt.Errorf("size extended attribute %q: %w", name, err)
		}
		value := make([]byte, valueSize)
		if valueSize > 0 {
			valueSize, err = unix.Fgetxattr(int(source.Fd()), name, value)
			if err != nil {
				return fmt.Errorf("read extended attribute %q: %w", name, err)
			}
			value = value[:valueSize]
		}
		if err := unix.Fsetxattr(int(replacement.Fd()), name, value, 0); err != nil {
			return fmt.Errorf("preserve extended attribute %q: %w", name, err)
		}
	}
	return nil
}

func agentExtendedAttributeRequiresRecomputation(name string) bool {
	// Linux IMA and EVM values authenticate file bytes and/or metadata. Copying
	// them to an inode containing rewritten bytes would preserve stale integrity
	// evidence, so a privileged invocation must fail closed instead.
	return name == "security.ima" || name == "security.evm"
}

func agentExtendedAttributeNames(file *os.File) ([]string, bool, error) {
	size, err := unix.Flistxattr(int(file.Fd()), nil)
	if errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EOPNOTSUPP) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if size == 0 {
		return nil, true, nil
	}
	names := make([]byte, size)
	size, err = unix.Flistxattr(int(file.Fd()), names)
	if err != nil {
		return nil, false, err
	}
	return splitNullTerminatedNames(names[:size]), true, nil
}

func splitNullTerminatedNames(raw []byte) []string {
	var names []string
	for len(raw) > 0 {
		end := 0
		for end < len(raw) && raw[end] != 0 {
			end++
		}
		if end > 0 {
			names = append(names, string(raw[:end]))
		}
		if end == len(raw) {
			break
		}
		raw = raw[end+1:]
	}
	return names
}

func syncAgentParentDirectory(path string) error {
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	syncErr := dir.Sync()
	return errors.Join(syncErr, dir.Close())
}

func commitAgentReplacement(locked *lockedAgentFile, _ *os.File, replacementPath string, replacementInfo os.FileInfo) (bool, error) {
	if err := locked.verifyUnchanged(); err != nil {
		return true, fmt.Errorf("verify destination before replacement: %w", err)
	}
	if err := verifyAgentReplacementPath(replacementPath, replacementInfo); err != nil {
		return false, err
	}
	if err := os.Rename(replacementPath, locked.path); err != nil {
		return true, fmt.Errorf("rename temp file: %w", err)
	}
	publishedInfo, err := os.Lstat(locked.path)
	if err != nil || !publishedInfo.Mode().IsRegular() || !os.SameFile(replacementInfo, publishedInfo) {
		// The path-only rename gap cannot be made into a portable descriptor CAS,
		// but never report success if an uncooperative directory writer won it.
		return false, fmt.Errorf("replacement destination changed during atomic publication")
	}
	return true, nil
}

// openAgentFileForMutation, lockAgentFile, and unlockAgentFile are the
// decomposed remote locking API. Production mutation uses
// openAndLockAgentFileForMutation (O_NOFOLLOW, non-blocking flock with
// timeout). These names remain so callers and tests can open/lock separately
// while still refusing to follow a final-component symlink.
func openAgentFileForMutation(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

func lockAgentFile(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_EX)
}

func unlockAgentFile(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_UN)
}
