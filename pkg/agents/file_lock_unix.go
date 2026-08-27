//go:build (darwin || linux) && !android

package agents

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
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

func verifyAgentFileHasSingleLink(file *os.File) error {
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return fmt.Errorf("inspect published file links: %w", err)
	}
	if stat.Nlink != 1 {
		return fmt.Errorf("published file has %d hard links, want 1", stat.Nlink)
	}
	return nil
}

// closeUnixAgentPrivateFileAfterFailure closes a candidate that cannot be
// returned safely and reports whether its never-unlinked private pathname still
// identifies the inode we created. Pathname cleanup is intentionally not
// attempted: an identity check followed by unlink would let a concurrent writer
// replace the name in between and trick bv into deleting peer data.
func closeUnixAgentPrivateFileAfterFailure(file *os.File, path string, expected os.FileInfo, operationErr error) error {
	if file == nil {
		return operationErr
	}
	closeErr := file.Close()
	pathInfo, pathErr := os.Lstat(path)

	var retentionErr error
	switch {
	case expected != nil && pathErr == nil && pathInfo.Mode().IsRegular() && os.SameFile(expected, pathInfo):
		retentionErr = fmt.Errorf("private replacement retained at %q for recovery", path)
	case expected == nil:
		retentionErr = fmt.Errorf("private replacement was allocated at %q but its retained identity could not be verified", path)
	case pathErr != nil:
		retentionErr = fmt.Errorf("private replacement was allocated at %q but its retained pathname could not be verified: %w", path, pathErr)
	default:
		retentionErr = fmt.Errorf("private replacement was allocated at %q but that pathname no longer identifies the created inode", path)
	}
	return errors.Join(operationErr, closeErr, retentionErr)
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

func prepareAgentReplacementMetadata(source, replacement *os.File, _ os.FileMode) error {
	if err := verifyUnixAgentReplacementPrivateState(replacement); err != nil {
		return err
	}
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
	if replacementStat.Nlink != 1 {
		return fmt.Errorf("refusing to publish replacement candidate with %d hard links", replacementStat.Nlink)
	}
	if replacementStat.Uid != sourceStat.Uid || replacementStat.Gid != sourceStat.Gid {
		if err := unix.Fchown(int(replacement.Fd()), int(sourceStat.Uid), int(sourceStat.Gid)); err != nil {
			return fmt.Errorf("preserve owner/group: %w", err)
		}
	}
	if err := verifyUnixAgentReplacementPrivateMetadata(source, replacement); err != nil {
		return err
	}
	if err := copyAgentExtendedAttributes(source, replacement); err != nil {
		return err
	}
	if err := verifyUnixAgentReplacementPrivateMetadata(source, replacement); err != nil {
		return err
	}
	if err := copyAgentPlatformFileFlags(source, replacement); err != nil {
		return err
	}
	if err := verifyUnixAgentReplacementPrivateMetadata(source, replacement); err != nil {
		return err
	}
	return nil
}

func verifyUnixAgentReplacementPrivateMetadata(source, replacement *os.File) error {
	if err := verifyUnixAgentReplacementMetadata(source, replacement, false); err != nil {
		return err
	}
	return verifyUnixAgentReplacementPrivateState(replacement)
}

func verifyUnixAgentReplacementPrivateState(replacement *os.File) error {
	var replacementStat unix.Stat_t
	if err := unix.Fstat(int(replacement.Fd()), &replacementStat); err != nil {
		return fmt.Errorf("restat private replacement metadata: %w", err)
	}
	if replacementStat.Nlink != 1 {
		return fmt.Errorf("refusing to prepare replacement candidate with %d hard links", replacementStat.Nlink)
	}
	if replacementStat.Mode&0o7777 != 0 {
		return fmt.Errorf("private replacement mode %#o is not 000", replacementStat.Mode&0o7777)
	}
	return nil
}

func verifyUnixAgentReplacementMetadata(source, replacement *os.File, requireMode bool) error {
	var sourceStat unix.Stat_t
	if err := unix.Fstat(int(source.Fd()), &sourceStat); err != nil {
		return fmt.Errorf("restat source metadata: %w", err)
	}
	var replacementStat unix.Stat_t
	if err := unix.Fstat(int(replacement.Fd()), &replacementStat); err != nil {
		return fmt.Errorf("restat replacement metadata: %w", err)
	}
	if replacementStat.Nlink != 1 {
		return fmt.Errorf("refusing to publish replacement candidate with %d hard links", replacementStat.Nlink)
	}
	if replacementStat.Uid != sourceStat.Uid || replacementStat.Gid != sourceStat.Gid {
		return fmt.Errorf(
			"replacement owner/group %d:%d differs from source %d:%d",
			replacementStat.Uid,
			replacementStat.Gid,
			sourceStat.Uid,
			sourceStat.Gid,
		)
	}
	if requireMode && replacementStat.Mode != sourceStat.Mode {
		return fmt.Errorf("replacement mode %#o differs from source %#o", replacementStat.Mode, sourceStat.Mode)
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
		if agentExtendedAttributeDefersAccess(name) {
			// A default ACL can be inherited even when O_CREAT is passed mode 000.
			// Its mask should be zero, but remove it while the candidate is private
			// and apply the source ACL only in the final access-enabling phase.
			if err := unix.Fremovexattr(int(replacement.Fd()), name); err != nil {
				return fmt.Errorf("remove deferred replacement access attribute %q: %w", name, err)
			}
			continue
		}
		if _, exists := sourceNameSet[name]; exists {
			continue
		}
		if err := unix.Fremovexattr(int(replacement.Fd()), name); err != nil {
			return fmt.Errorf("remove replacement-only extended attribute %q: %w", name, err)
		}
	}

	for _, name := range sourceNames {
		if agentExtendedAttributeDefersAccess(name) {
			continue
		}
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

func verifyAgentExtendedAttributes(source, replacement *os.File) error {
	sourceNames, sourceSupported, err := agentExtendedAttributeNames(source)
	if err != nil {
		return fmt.Errorf("list finalized source extended attributes: %w", err)
	}
	replacementNames, replacementSupported, err := agentExtendedAttributeNames(replacement)
	if err != nil {
		return fmt.Errorf("list finalized replacement extended attributes: %w", err)
	}
	if sourceSupported != replacementSupported {
		return fmt.Errorf("source and finalized replacement disagree on extended-attribute support")
	}
	if !sourceSupported {
		return nil
	}

	replacementSet := make(map[string]struct{}, len(replacementNames))
	for _, name := range replacementNames {
		replacementSet[name] = struct{}{}
	}
	if len(sourceNames) != len(replacementNames) {
		return fmt.Errorf("finalized replacement extended-attribute set differs from source")
	}
	for _, name := range sourceNames {
		if agentExtendedAttributeRequiresRecomputation(name) {
			return fmt.Errorf("refusing to replace agent file with content-bound extended attribute %q", name)
		}
		if _, ok := replacementSet[name]; !ok {
			return fmt.Errorf("finalized replacement is missing source extended attribute %q", name)
		}
		sourceValue, err := agentExtendedAttributeValue(source, name)
		if err != nil {
			return fmt.Errorf("read finalized source extended attribute %q: %w", name, err)
		}
		replacementValue, err := agentExtendedAttributeValue(replacement, name)
		if err != nil {
			return fmt.Errorf("read finalized replacement extended attribute %q: %w", name, err)
		}
		if !bytes.Equal(sourceValue, replacementValue) {
			return fmt.Errorf("finalized replacement extended attribute %q differs from source", name)
		}
	}
	return nil
}

type agentExtendedAttributeSnapshot struct {
	name  string
	value []byte
}

func snapshotAgentExtendedAttributes(file *os.File) ([]agentExtendedAttributeSnapshot, bool, error) {
	names, supported, err := agentExtendedAttributeNames(file)
	if err != nil || !supported {
		return nil, supported, err
	}
	sort.Strings(names)
	snapshot := make([]agentExtendedAttributeSnapshot, 0, len(names))
	for _, name := range names {
		value, err := agentExtendedAttributeValue(file, name)
		if err != nil {
			return nil, true, fmt.Errorf("snapshot extended attribute %q: %w", name, err)
		}
		snapshot = append(snapshot, agentExtendedAttributeSnapshot{name: name, value: value})
	}
	return snapshot, true, nil
}

func sameAgentExtendedAttributeSnapshot(
	a []agentExtendedAttributeSnapshot,
	aSupported bool,
	b []agentExtendedAttributeSnapshot,
	bSupported bool,
) bool {
	if aSupported != bSupported || len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].name != b[i].name || !bytes.Equal(a[i].value, b[i].value) {
			return false
		}
	}
	return true
}

func agentExtendedAttributeValue(file *os.File, name string) ([]byte, error) {
	size, err := unix.Fgetxattr(int(file.Fd()), name, nil)
	if err != nil {
		return nil, err
	}
	value := make([]byte, size)
	if size == 0 {
		return value, nil
	}
	read, err := unix.Fgetxattr(int(file.Fd()), name, value)
	if err != nil {
		return nil, err
	}
	return value[:read], nil
}

func validateAgentSourceExtendedAttributesForReplacement(source *os.File) error {
	sourceNames, supported, err := agentExtendedAttributeNames(source)
	if err != nil {
		return fmt.Errorf("list source extended attributes before replacement: %w", err)
	}
	if !supported {
		return nil
	}
	for _, name := range sourceNames {
		if agentExtendedAttributeRequiresRecomputation(name) {
			return fmt.Errorf("refusing to replace agent file with content-bound extended attribute %q", name)
		}
	}
	return nil
}

var preflightAgentSourceExtendedAttributes = validateAgentSourceExtendedAttributesForReplacement

func agentExtendedAttributeRequiresRecomputation(name string) bool {
	// Linux IMA/EVM values authenticate file bytes and/or metadata, while file
	// capabilities grant executable privileges to particular content. Copying
	// any of them to an inode containing rewritten bytes would preserve stale
	// integrity or privilege evidence. Darwin generic code signatures likewise
	// store content-sealing data in com.apple.cs.* attributes. Fail closed rather
	// than transferring any of these attributes to different bytes.
	return name == "security.ima" ||
		name == "security.evm" ||
		name == "security.capability" ||
		strings.HasPrefix(name, "com.apple.cs.")
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

func makeAgentReplacementPrivateAfterFailure(file *os.File) error {
	if file == nil {
		return nil
	}
	removableErr := makeAgentReplacementRemovable(file)
	chmodErr := file.Chmod(0)
	syncErr := file.Sync()
	return errors.Join(removableErr, chmodErr, syncErr)
}

var (
	verifyPublishedUnixAgentReplacement = verifyPublishedAgentReplacement
	verifyPublishedUnixAgentMetadata    = verifyFinalizedAgentReplacementMetadata
	syncPublishedUnixAgentDirectory     = syncAgentParentDirectory
	verifyFinalizedUnixAgentReplacement = verifyFinalizedAgentReplacementMetadata
	exchangeUnixAgentFilePaths          = exchangeAgentFilePaths
	makeUnixAgentDisplacedSourcePrivate = makeAgentDisplacedSourcePrivate
	truncateUnixAgentDisplacedSource    = func(file *os.File) error { return file.Truncate(0) }
	syncUnixAgentDisplacedSource        = func(file *os.File) error { return file.Sync() }
	closeUnixAgentRecoveryFile          = func(file *os.File) error { return file.Close() }
	lstatUnixAgentRecoveryPath          = os.Lstat
	agentFileSupportsProtectionClass    = func(*os.File) (bool, error) { return false, nil }
	callAgentProtectionClassFcntl       = func(uintptr, int, int) (int, error) { return -1, unix.ENOTSUP }
)

func revalidateAllocatedUnixAgentRecoveryPath(path string, openedInfo os.FileInfo) error {
	pathInfo, err := lstatUnixAgentRecoveryPath(path)
	if err != nil {
		return fmt.Errorf("revalidate allocated original recovery path: %w", err)
	}
	if openedInfo == nil || !pathInfo.Mode().IsRegular() || !os.SameFile(openedInfo, pathInfo) {
		return fmt.Errorf("allocated original recovery path no longer identifies the created file")
	}
	return nil
}

// retainUnixAgentOriginalForRecovery materializes the locked source bytes only
// after publication has succeeded but the exchange did not displace the exact
// locked source. Every created recovery name is mode 000 and is never removed
// through a racy path.
func retainUnixAgentOriginalForRecovery(locked *lockedAgentFile) (string, error) {
	if locked == nil || len(locked.content) > maxAgentFileBytes {
		return "", fmt.Errorf("locked original bytes are unavailable")
	}
	// Use the platform recovery allocator so Darwin verifies the recovery inode's
	// inherited ACL against the locked source before any original byte is written.
	// Recovery retains the already-locked original bytes, so it deliberately does
	// not rerun replacement-only content-bound-xattr or inode-flag policy checks:
	// a metadata race that triggered the post-publication refusal must not also
	// prevent the last named copy of the original bytes from being materialized.
	file, path, openedInfo, err := createAgentRecoveryFile(locked)
	if err != nil {
		return "", fmt.Errorf("create access-safe original recovery file: %w", err)
	}
	fail := func(operationErr error) (string, error) {
		closeErr := closeUnixAgentRecoveryFile(file)
		identityErr := revalidateAllocatedUnixAgentRecoveryPath(path, openedInfo)
		// Once the secure allocator returns a path, never erase it from the
		// diagnostic. Even when close or identity revalidation fails, the caller
		// must know which never-unlinked name to inspect without assuming that its
		// current identity, exact bytes, or durability were established.
		return path, errors.Join(operationErr, closeErr, identityErr)
	}
	if openedInfo.Mode().Perm() != 0 {
		return fail(fmt.Errorf("original recovery mode is not 000"))
	}

	written, err := file.Write(locked.content)
	if err != nil {
		return fail(fmt.Errorf("write original recovery bytes: %w", err))
	}
	if written != len(locked.content) {
		return fail(fmt.Errorf("write original recovery bytes: %w", io.ErrShortWrite))
	}
	if err := file.Sync(); err != nil {
		return fail(fmt.Errorf("sync original recovery bytes: %w", err))
	}
	completedBefore, err := file.Stat()
	if err != nil {
		return fail(fmt.Errorf("stat completed original recovery: %w", err))
	}
	pathInfo, err := lstatUnixAgentRecoveryPath(path)
	if err != nil || !sameAgentFileSnapshot(completedBefore, pathInfo) {
		if err != nil {
			return fail(fmt.Errorf("stat completed original recovery path: %w", err))
		}
		return fail(fmt.Errorf("original recovery path changed before verification"))
	}
	content, err := readAgentFileExactly(file, int64(len(locked.content)))
	if err != nil {
		return fail(fmt.Errorf("verify original recovery bytes: %w", err))
	}
	completedAfter, err := file.Stat()
	if err != nil {
		return fail(fmt.Errorf("restat completed original recovery: %w", err))
	}
	if !bytes.Equal(content, locked.content) || !sameAgentFileSnapshot(completedBefore, completedAfter) {
		return fail(fmt.Errorf("original recovery changed during verification"))
	}
	if err := closeUnixAgentRecoveryFile(file); err != nil {
		return path, errors.Join(
			fmt.Errorf("close original recovery file: %w", err),
			revalidateAllocatedUnixAgentRecoveryPath(path, openedInfo),
		)
	}
	pathInfo, err = lstatUnixAgentRecoveryPath(path)
	if err != nil || !sameAgentFileSnapshot(completedAfter, pathInfo) {
		if err != nil {
			return path, fmt.Errorf("stat closed original recovery path: %w", err)
		}
		return path, fmt.Errorf("original recovery path changed after close")
	}
	if err := syncAgentParentDirectory(path); err != nil {
		return path, fmt.Errorf("sync original recovery directory: %w", err)
	}
	return path, nil
}

func unixAgentPostPublicationFailure(locked *lockedAgentFile, publicationErr error) error {
	recoveryPath, recoveryErr := retainUnixAgentOriginalForRecovery(locked)
	if recoveryErr == nil {
		return fmt.Errorf("%w; original bytes retained at %q", publicationErr, recoveryPath)
	}
	if recoveryPath != "" {
		return fmt.Errorf("%w; original recovery path allocated at %q but its current identity, exact bytes, or directory durability were not fully verified: %v", publicationErr, recoveryPath, recoveryErr)
	}
	return fmt.Errorf("%w; failed to retain original recovery bytes: %v", publicationErr, recoveryErr)
}

// verifyUnixAgentDisplacedSource proves that an atomic exchange moved the exact
// locked source inode, unchanged except for the ctime update caused by the
// exchange itself, to the candidate's former private name. This is the second
// half of the publication CAS: validating only the new destination would miss a
// lock-ignoring editor that replaced or modified the destination in the final
// preflight/exchange gap.
func verifyUnixAgentDisplacedSource(locked *lockedAgentFile, displacedPath string) error {
	if locked == nil || locked.file == nil {
		return fmt.Errorf("locked source handle is unavailable")
	}

	beforeInfo, err := locked.file.Stat()
	if err != nil {
		return fmt.Errorf("stat exchanged locked source: %w", err)
	}
	beforeMetadata, err := snapshotAgentFileMetadata(locked.file)
	if err != nil {
		return fmt.Errorf("snapshot exchanged locked source metadata: %w", err)
	}
	pathBefore, err := os.Lstat(displacedPath)
	if err != nil {
		return fmt.Errorf("stat exchanged-away destination path: %w", err)
	}
	if !pathBefore.Mode().IsRegular() || !os.SameFile(beforeInfo, pathBefore) {
		return fmt.Errorf("exchanged-away destination does not identify the locked source inode")
	}
	if !sameAgentFileSnapshot(locked.info, beforeInfo) {
		return fmt.Errorf("exchanged locked source snapshot differs from the pre-publication source")
	}
	if !sameAgentFileMetadataAcrossExchange(locked.metadata, beforeMetadata) {
		return fmt.Errorf("exchanged locked source metadata differs from the pre-publication source")
	}
	if err := verifyAgentFileHasSingleLink(locked.file); err != nil {
		return fmt.Errorf("verify exchanged locked source links: %w", err)
	}

	content, err := readAgentFileExactly(locked.file, int64(len(locked.content)))
	if err != nil {
		return fmt.Errorf("read exchanged locked source: %w", err)
	}
	afterInfo, err := locked.file.Stat()
	if err != nil {
		return fmt.Errorf("restat exchanged locked source: %w", err)
	}
	afterMetadata, err := snapshotAgentFileMetadata(locked.file)
	if err != nil {
		return fmt.Errorf("resnapshot exchanged locked source metadata: %w", err)
	}
	pathAfter, err := os.Lstat(displacedPath)
	if err != nil {
		return fmt.Errorf("restat exchanged-away destination path: %w", err)
	}
	if !bytes.Equal(content, locked.content) {
		return fmt.Errorf("exchanged locked source bytes differ from the pre-publication source")
	}
	if !sameAgentFileSnapshot(beforeInfo, afterInfo) ||
		!sameAgentFileSnapshot(afterInfo, pathAfter) ||
		!sameAgentFileMetadata(beforeMetadata, afterMetadata) {
		return fmt.Errorf("exchanged locked source changed during verification")
	}
	if err := verifyAgentFileHasSingleLink(locked.file); err != nil {
		return fmt.Errorf("reverify exchanged locked source links: %w", err)
	}
	return nil
}

func verifyUnixAgentPrivateDisplacedSource(locked *lockedAgentFile, displacedPath string) error {
	if locked == nil || locked.file == nil {
		return fmt.Errorf("locked source handle is unavailable")
	}
	if err := verifyAgentDisplacedSourcePrivate(locked.file); err != nil {
		return err
	}

	beforeInfo, err := locked.file.Stat()
	if err != nil {
		return fmt.Errorf("stat private displaced source: %w", err)
	}
	beforeMetadata, err := snapshotAgentFileMetadata(locked.file)
	if err != nil {
		return fmt.Errorf("snapshot private displaced source metadata: %w", err)
	}
	pathBefore, err := os.Lstat(displacedPath)
	if err != nil {
		return fmt.Errorf("stat private displaced source path: %w", err)
	}
	if !pathBefore.Mode().IsRegular() || !sameAgentFileSnapshot(beforeInfo, pathBefore) {
		return fmt.Errorf("private displaced source path does not identify the locked source inode")
	}

	content, err := readAgentFileExactly(locked.file, int64(len(locked.content)))
	if err != nil {
		return fmt.Errorf("read private displaced source: %w", err)
	}
	afterInfo, err := locked.file.Stat()
	if err != nil {
		return fmt.Errorf("restat private displaced source: %w", err)
	}
	afterMetadata, err := snapshotAgentFileMetadata(locked.file)
	if err != nil {
		return fmt.Errorf("resnapshot private displaced source metadata: %w", err)
	}
	pathAfter, err := os.Lstat(displacedPath)
	if err != nil {
		return fmt.Errorf("restat private displaced source path: %w", err)
	}
	if !bytes.Equal(content, locked.content) {
		return fmt.Errorf("private displaced source bytes differ from the locked source")
	}
	if !sameAgentFileSnapshot(beforeInfo, afterInfo) ||
		!sameAgentFileSnapshot(afterInfo, pathAfter) ||
		!sameAgentFileMetadata(beforeMetadata, afterMetadata) {
		return fmt.Errorf("private displaced source changed during verification")
	}
	return verifyAgentDisplacedSourcePrivate(locked.file)
}

func verifyUnixAgentEmptyPrivateDisplacedSource(locked *lockedAgentFile, displacedPath string) error {
	if locked == nil || locked.file == nil {
		return fmt.Errorf("locked source handle is unavailable")
	}
	if err := verifyAgentDisplacedSourcePrivate(locked.file); err != nil {
		return err
	}

	beforeInfo, err := locked.file.Stat()
	if err != nil {
		return fmt.Errorf("stat truncated displaced source: %w", err)
	}
	pathBefore, err := os.Lstat(displacedPath)
	if err != nil {
		return fmt.Errorf("stat truncated displaced source path: %w", err)
	}
	if beforeInfo.Size() != 0 || !pathBefore.Mode().IsRegular() || !sameAgentFileSnapshot(beforeInfo, pathBefore) {
		return fmt.Errorf("truncated displaced source path is nonempty or no longer identifies the locked source inode")
	}

	afterInfo, err := locked.file.Stat()
	if err != nil {
		return fmt.Errorf("restat truncated displaced source: %w", err)
	}
	pathAfter, err := os.Lstat(displacedPath)
	if err != nil {
		return fmt.Errorf("restat truncated displaced source path: %w", err)
	}
	if !sameAgentFileSnapshot(beforeInfo, afterInfo) || !sameAgentFileSnapshot(afterInfo, pathAfter) {
		return fmt.Errorf("truncated displaced source changed during verification")
	}
	return verifyAgentDisplacedSourcePrivate(locked.file)
}

func openUnixAgentDisplacedSourceForTruncation(locked *lockedAgentFile, displacedPath string) (*os.File, error) {
	if err := verifyUnixAgentPrivateDisplacedSource(locked, displacedPath); err != nil {
		return nil, err
	}
	// The lock descriptor is intentionally read-only so mode-0400 sources remain
	// editable through atomic replacement. Briefly grant owner-write only (never
	// read access), open a second descriptor, and immediately restore mode 000.
	// Nothing is truncated until that descriptor is proven to identify both the
	// still-open locked inode and its cryptorandom private path.
	if err := locked.file.Chmod(0o200); err != nil {
		return nil, fmt.Errorf("make displaced source write-only for secure reopen: %w", err)
	}
	fd, openErr := unix.Open(displacedPath, unix.O_WRONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	restoreErr := locked.file.Chmod(0)
	syncErr := locked.file.Sync()
	if openErr != nil {
		return nil, errors.Join(
			fmt.Errorf("open private displaced source for identity-bound truncation: %w", openErr),
			restoreErr,
			syncErr,
		)
	}
	writeFile := os.NewFile(uintptr(fd), displacedPath)
	fail := func(operationErr error) (*os.File, error) {
		return nil, errors.Join(operationErr, restoreErr, syncErr, writeFile.Close())
	}
	if restoreErr != nil || syncErr != nil {
		return fail(fmt.Errorf("restore displaced source to durable mode 000 after secure reopen"))
	}

	lockedInfo, err := locked.file.Stat()
	if err != nil {
		return fail(fmt.Errorf("stat locked displaced source after secure reopen: %w", err))
	}
	writeInfo, err := writeFile.Stat()
	if err != nil {
		return fail(fmt.Errorf("stat writable displaced-source handle: %w", err))
	}
	pathInfo, err := os.Lstat(displacedPath)
	if err != nil {
		return fail(fmt.Errorf("stat writable displaced-source path: %w", err))
	}
	if !writeInfo.Mode().IsRegular() || !os.SameFile(lockedInfo, writeInfo) || !sameAgentFileSnapshot(writeInfo, pathInfo) {
		return fail(fmt.Errorf("writable displaced-source handle is not bound to the locked private path"))
	}
	if err := verifyAgentFileHasSingleLink(writeFile); err != nil {
		return fail(fmt.Errorf("verify writable displaced-source links: %w", err))
	}
	if err := verifyUnixAgentPrivateDisplacedSource(locked, displacedPath); err != nil {
		return fail(fmt.Errorf("reverify private displaced source after secure reopen: %w", err))
	}
	return writeFile, nil
}

// unixAgentPostExchangeFailure never tries to undo a completed exchange. A
// second swap would race the same uncooperative writer and could overwrite yet
// another peer. When the displaced name still proves the locked original, name
// it directly. Otherwise retain the possibly-peer data at that name and create a
// separate mode-000 copy of the original locked bytes for recovery.
func unixAgentPostExchangeFailure(locked *lockedAgentFile, displacedPath string, publicationErr error) error {
	publicationErr = fmt.Errorf("replacement publication is partial: %w", publicationErr)
	if err := verifyUnixAgentDisplacedSource(locked, displacedPath); err != nil {
		return unixAgentPostPublicationFailure(
			locked,
			fmt.Errorf("%w; unexpected displaced data retained at %q: %v", publicationErr, displacedPath, err),
		)
	}
	if err := makeUnixAgentDisplacedSourcePrivate(locked.file); err != nil {
		return fmt.Errorf("%w; original bytes retained at %q but could not be made access-private: %v", publicationErr, displacedPath, err)
	}
	if err := verifyUnixAgentPrivateDisplacedSource(locked, displacedPath); err != nil {
		return unixAgentPostPublicationFailure(
			locked,
			fmt.Errorf("%w; displaced-original path %q failed private revalidation: %v", publicationErr, displacedPath, err),
		)
	}
	return fmt.Errorf("%w; original bytes retained at %q in access-private form", publicationErr, displacedPath)
}

func unixAgentPostPrivateExchangeFailure(locked *lockedAgentFile, displacedPath string, publicationErr error) error {
	publicationErr = fmt.Errorf("replacement publication is partial: %w", publicationErr)
	if err := verifyUnixAgentPrivateDisplacedSource(locked, displacedPath); err != nil {
		return unixAgentPostPublicationFailure(
			locked,
			fmt.Errorf("%w; private displaced-original path %q failed revalidation: %v", publicationErr, displacedPath, err),
		)
	}
	return fmt.Errorf("%w; original bytes retained at %q in access-private form", publicationErr, displacedPath)
}

func commitAgentReplacement(locked *lockedAgentFile, replacementFile *os.File, replacementPath string, replacementInfo os.FileInfo, expectedContent []byte) (bool, error) {
	if err := locked.verifyUnchanged(); err != nil {
		return false, fmt.Errorf("verify destination before replacement: %w", err)
	}
	if err := verifyAgentReplacementPath(replacementPath, replacementInfo); err != nil {
		return false, err
	}
	var replacementStat unix.Stat_t
	if err := unix.Fstat(int(replacementFile.Fd()), &replacementStat); err != nil {
		return false, fmt.Errorf("stat replacement immediately before publication: %w", err)
	}
	if replacementStat.Nlink != 1 {
		return false, fmt.Errorf("refusing to publish replacement candidate with %d hard links", replacementStat.Nlink)
	}
	captureBefore, err := replacementFile.Stat()
	if err != nil || !sameAgentFileSnapshot(replacementInfo, captureBefore) {
		if err != nil {
			return false, fmt.Errorf("stat replacement before byte capture: %w", err)
		}
		return false, fmt.Errorf("replacement changed before byte capture")
	}
	capturedContent, err := readAgentFileExactly(replacementFile, replacementInfo.Size())
	if err != nil {
		return false, fmt.Errorf("capture replacement bytes before commit: %w", err)
	}
	captureAfter, err := replacementFile.Stat()
	if err != nil || !sameAgentFileSnapshot(captureBefore, captureAfter) {
		if err != nil {
			return false, fmt.Errorf("restat replacement after byte capture: %w", err)
		}
		return false, fmt.Errorf("replacement changed during byte capture")
	}
	if !bytes.Equal(capturedContent, expectedContent) {
		return false, fmt.Errorf("replacement bytes differ from intended content before commit")
	}
	if err := verifyAgentReplacementPath(replacementPath, captureAfter); err != nil {
		return false, err
	}
	if err := unix.Fstat(int(replacementFile.Fd()), &replacementStat); err != nil {
		return false, fmt.Errorf("restat replacement immediately before publication: %w", err)
	}
	if replacementStat.Nlink != 1 {
		return false, fmt.Errorf("refusing to publish replacement candidate with %d hard links", replacementStat.Nlink)
	}
	if err := verifyFinalizedUnixAgentReplacement(locked.file, replacementFile); err != nil {
		return false, fmt.Errorf("verify finalized replacement metadata immediately before publication: %w", err)
	}
	if err := locked.verifyUnchanged(); err != nil {
		return false, fmt.Errorf("verify destination immediately before replacement: %w", err)
	}
	if err := exchangeUnixAgentFilePaths(replacementPath, locked.path); err != nil {
		return false, fmt.Errorf("exchange replacement with destination: %w", err)
	}
	publishedInfo, err := os.Lstat(locked.path)
	if err != nil || !publishedInfo.Mode().IsRegular() || !os.SameFile(replacementInfo, publishedInfo) {
		return true, unixAgentPostExchangeFailure(locked, replacementPath, fmt.Errorf("replacement destination changed during atomic exchange"))
	}
	if err := verifyUnixAgentDisplacedSource(locked, replacementPath); err != nil {
		return true, unixAgentPostExchangeFailure(locked, replacementPath, fmt.Errorf("atomic exchange displaced an unexpected destination: %w", err))
	}
	if err := verifyPublishedUnixAgentReplacement(locked.path, replacementInfo, expectedContent); err != nil {
		return true, unixAgentPostExchangeFailure(locked, replacementPath, fmt.Errorf("verify published replacement: %w", err))
	}
	// The generic published-file verifier binds the pathname, inode, and intended
	// bytes, but its metadata snapshots only prove stability during that read.
	// Recompare the already-published inode with the still-open locked source so a
	// candidate xattr, ACL, or platform-flag change in the final verify/exchange gap
	// cannot be accepted merely because it became stable before the path reopened.
	if err := verifyPublishedUnixAgentMetadata(locked.file, replacementFile); err != nil {
		return true, unixAgentPostExchangeFailure(locked, replacementPath, fmt.Errorf("verify published replacement metadata: %w", err))
	}
	publishedMetadata, err := snapshotFinalizedAgentFileMetadata(replacementFile)
	if err != nil {
		return true, unixAgentPostExchangeFailure(locked, replacementPath, fmt.Errorf("snapshot published replacement metadata: %w", err))
	}
	if err := makeUnixAgentDisplacedSourcePrivate(locked.file); err != nil {
		return true, fmt.Errorf("replacement was published, but original bytes retained at %q could not be made access-private; publication is partial: %w", replacementPath, err)
	}
	if err := verifyUnixAgentPrivateDisplacedSource(locked, replacementPath); err != nil {
		return true, unixAgentPostPrivateExchangeFailure(locked, replacementPath, fmt.Errorf("verify private displaced original: %w", err))
	}
	// Identity-bound unlink is unavailable, so the exact displaced source name
	// must remain. Reverify the published candidate after privating that inode; a
	// peer may still race either path while the advisory lock is held.
	if err := verifyPublishedUnixAgentReplacement(locked.path, replacementInfo, expectedContent); err != nil {
		return true, unixAgentPostPrivateExchangeFailure(locked, replacementPath, fmt.Errorf("reverify published replacement: %w", err))
	}
	currentPublishedMetadata, err := snapshotFinalizedAgentFileMetadata(replacementFile)
	if err != nil || !sameFinalizedAgentFileMetadata(publishedMetadata, currentPublishedMetadata) {
		if err != nil {
			return true, unixAgentPostPrivateExchangeFailure(locked, replacementPath, fmt.Errorf("resnapshot published replacement metadata: %w", err))
		}
		return true, unixAgentPostPrivateExchangeFailure(locked, replacementPath, fmt.Errorf("published replacement metadata changed while the displaced original was made private"))
	}
	if err := syncPublishedUnixAgentDirectory(locked.path); err != nil {
		return true, unixAgentPostPrivateExchangeFailure(locked, replacementPath, fmt.Errorf("sync replacement directory: %w", err))
	}

	// Only after the published name, bytes, metadata, and parent-directory entry
	// are durable do we destroy the old bytes. Truncation is performed through a
	// write-only handle proven to identify the still-open locked inode and its
	// private pathname, so a peer cannot make bv erase a substituted file. The
	// cryptorandom name and empty inode remain as the bounded cost of lacking an
	// identity-bound unlink primitive.
	displacedWriteFile, err := openUnixAgentDisplacedSourceForTruncation(locked, replacementPath)
	if err != nil {
		return true, unixAgentPostPrivateExchangeFailure(
			locked,
			replacementPath,
			fmt.Errorf("replacement was published and directory-synced, but the private displaced original could not be opened safely for truncation; publication is partial: %w", err),
		)
	}
	if err := truncateUnixAgentDisplacedSource(displacedWriteFile); err != nil {
		closeErr := displacedWriteFile.Close()
		return true, unixAgentPostPrivateExchangeFailure(
			locked,
			replacementPath,
			fmt.Errorf("replacement was published and directory-synced, but truncating the private displaced original failed; publication is partial: %w", errors.Join(err, closeErr)),
		)
	}
	if err := syncUnixAgentDisplacedSource(displacedWriteFile); err != nil {
		closeErr := displacedWriteFile.Close()
		return true, unixAgentPostPublicationFailure(
			locked,
			fmt.Errorf("replacement was published, but syncing the truncated private displaced original at %q failed; publication is partial: %w", replacementPath, errors.Join(err, closeErr)),
		)
	}
	if err := displacedWriteFile.Close(); err != nil {
		return true, unixAgentPostPublicationFailure(
			locked,
			fmt.Errorf("replacement was published, but closing the truncated private displaced-original handle for %q failed; publication is partial: %w", replacementPath, err),
		)
	}
	if err := verifyUnixAgentEmptyPrivateDisplacedSource(locked, replacementPath); err != nil {
		return true, unixAgentPostPublicationFailure(
			locked,
			fmt.Errorf("replacement was published, but the truncated private displaced-original path %q failed verification; publication is partial: %w", replacementPath, err),
		)
	}
	if err := verifyPublishedUnixAgentReplacement(locked.path, replacementInfo, expectedContent); err != nil {
		return true, unixAgentPostPublicationFailure(
			locked,
			fmt.Errorf("replacement was published and the old bytes were truncated, but final published-file verification failed; publication is partial: %w", err),
		)
	}
	finalPublishedMetadata, err := snapshotFinalizedAgentFileMetadata(replacementFile)
	if err != nil || !sameFinalizedAgentFileMetadata(publishedMetadata, finalPublishedMetadata) {
		if err != nil {
			return true, unixAgentPostPublicationFailure(
				locked,
				fmt.Errorf("replacement was published and the old bytes were truncated, but final metadata snapshot failed; publication is partial: %w", err),
			)
		}
		return true, unixAgentPostPublicationFailure(
			locked,
			fmt.Errorf("replacement was published and the old bytes were truncated, but its finalized metadata changed; publication is partial"),
		)
	}
	return true, nil
}
