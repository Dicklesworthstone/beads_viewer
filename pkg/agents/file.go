package agents

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

var (
	errAgentFileBusy     = errors.New("agent file is busy with another bv edit")
	errAgentFileChanged  = errors.New("agent file changed while the bv edit was being prepared")
	errAgentFileTooLarge = errors.New("agent file exceeds the safe mutation size limit")
)

const (
	agentFileLockTimeout = 2 * time.Second
	maxAgentFileBytes    = 16 << 20
)

type lockedAgentFile struct {
	requestedPath string
	requestedInfo os.FileInfo
	path          string
	file          *os.File
	unlock        func() error
	info          os.FileInfo
	metadata      agentFileMetadataSnapshot
	content       []byte
}

// lockAgentFileForMutation opens and locks the exact file whose bytes will be
// transformed. The lock serializes cooperating bv processes. Snapshot checks
// also reject changes already visible before commit, but no portable pathname
// operation can exclude a lock-ignoring editor in the final verify/rename gap.
func lockAgentFileForMutation(filePath string) (*lockedAgentFile, error) {
	requestedPath, err := filepath.Abs(filePath)
	if err != nil {
		return nil, fmt.Errorf("resolve agent-file path: %w", err)
	}
	requestedPath = filepath.Clean(requestedPath)
	deadline := time.Now().Add(agentFileLockTimeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, errAgentFileBusy
		}

		mutationPath, requestedInfo, err := resolveAgentMutationPath(requestedPath)
		if err != nil {
			return nil, err
		}
		file, unlock, err := openAndLockAgentFileForMutation(mutationPath, remaining)
		if err != nil {
			return nil, fmt.Errorf("lock file: %w", err)
		}

		closeLocked := func() {
			_ = unlock()
			_ = file.Close()
		}
		beforeInfo, beforeStatErr := file.Stat()
		pathInfo, pathStatErr := agentFilePathInfo(mutationPath)
		if beforeStatErr != nil || pathStatErr != nil {
			closeLocked()
			if beforeStatErr != nil {
				return nil, beforeStatErr
			}
			return nil, pathStatErr
		}
		if beforeInfo.Mode()&os.ModeSymlink != 0 || pathInfo.Mode()&os.ModeSymlink != 0 {
			closeLocked()
			return nil, fmt.Errorf("resolved agent-file target %q is still a symbolic link", mutationPath)
		}
		if !beforeInfo.Mode().IsRegular() || !pathInfo.Mode().IsRegular() {
			closeLocked()
			return nil, fmt.Errorf("refusing to replace non-regular agent file %q", mutationPath)
		}
		if !os.SameFile(beforeInfo, pathInfo) {
			// A prior atomic bv save can leave a waiter holding the displaced
			// inode. Reopen the current pathname before reading stale bytes while
			// the overall deadline is still live.
			closeLocked()
			continue
		}
		if requestedInfo.Mode()&os.ModeSymlink == 0 && !os.SameFile(requestedInfo, beforeInfo) {
			// The requested final component started as a regular file but was
			// redirected during symlink resolution. Never let that race turn a
			// privileged bv edit into a read or write through attacker-chosen data.
			closeLocked()
			return nil, fmt.Errorf("%w: requested regular file changed during path resolution", errAgentFileChanged)
		}
		content, readErr := readAgentFileExactly(file, beforeInfo.Size())
		afterInfo, afterStatErr := file.Stat()
		pathInfo, pathStatErr = agentFilePathInfo(mutationPath)
		if readErr != nil || afterStatErr != nil || pathStatErr != nil {
			closeLocked()
			if readErr != nil {
				return nil, readErr
			}
			if afterStatErr != nil {
				return nil, afterStatErr
			}
			return nil, pathStatErr
		}
		if !sameAgentFileSnapshot(beforeInfo, afterInfo) || int64(len(content)) != afterInfo.Size() ||
			!pathInfo.Mode().IsRegular() || !os.SameFile(afterInfo, pathInfo) {
			closeLocked()
			return nil, errAgentFileChanged
		}
		metadata, err := snapshotAgentFileMetadata(file)
		if err != nil {
			closeLocked()
			return nil, fmt.Errorf("snapshot agent-file metadata: %w", err)
		}

		locked := &lockedAgentFile{
			requestedPath: requestedPath,
			requestedInfo: requestedInfo,
			path:          mutationPath,
			file:          file,
			unlock:        unlock,
			info:          afterInfo,
			metadata:      metadata,
			content:       content,
		}
		if err := locked.verifyRequestedPath(afterInfo); err != nil {
			closeLocked()
			return nil, err
		}
		return locked, nil
	}
}

// resolveAgentMutationPath refuses a symbolic-link final component. Mutating a
// discovered AGENTS.md through a link can escape the repository while the CLI
// confirmation names only the link, not the external target.
func resolveAgentMutationPath(requestedPath string) (string, os.FileInfo, error) {
	requestedInfo, err := agentFilePathInfo(requestedPath)
	if err != nil {
		return "", nil, err
	}
	if requestedInfo.Mode()&os.ModeSymlink != 0 {
		return "", nil, fmt.Errorf("refusing to mutate symbolic-link agent file %q", requestedPath)
	}
	if !requestedInfo.Mode().IsRegular() {
		return "", nil, fmt.Errorf("refusing to replace non-regular agent file %q", requestedPath)
	}
	return requestedPath, requestedInfo, nil
}

func (f *lockedAgentFile) close() error {
	if f == nil {
		return nil
	}
	var closeErr error
	if f.unlock != nil {
		closeErr = errors.Join(closeErr, f.unlock())
		f.unlock = nil
	}
	if f.file != nil {
		closeErr = errors.Join(closeErr, f.file.Close())
		f.file = nil
	}
	return closeErr
}

func (f *lockedAgentFile) verifyUnchanged() error {
	if f == nil || f.file == nil {
		return fmt.Errorf("%w: locked source handle is closed", errAgentFileChanged)
	}
	currentInfo, err := f.file.Stat()
	if err != nil {
		return fmt.Errorf("%w: stat locked source: %v", errAgentFileChanged, err)
	}
	if !os.SameFile(f.info, currentInfo) {
		return fmt.Errorf("%w: locked source identity changed", errAgentFileChanged)
	}
	if f.info.Mode() != currentInfo.Mode() {
		return fmt.Errorf("%w: destination mode changed from %s to %s", errAgentFileChanged, f.info.Mode(), currentInfo.Mode())
	}
	content, err := readAgentFileExactly(f.file, currentInfo.Size())
	if err != nil {
		return fmt.Errorf("%w: reread locked source: %v", errAgentFileChanged, err)
	}
	afterReadInfo, err := f.file.Stat()
	if err != nil || !sameAgentFileSnapshot(currentInfo, afterReadInfo) || int64(len(content)) != afterReadInfo.Size() {
		return fmt.Errorf("%w: destination changed while it was being verified", errAgentFileChanged)
	}
	if !bytes.Equal(f.content, content) {
		return fmt.Errorf("%w: destination bytes changed", errAgentFileChanged)
	}
	currentMetadata, err := snapshotAgentFileMetadata(f.file)
	if err != nil {
		return fmt.Errorf("%w: snapshot current metadata: %v", errAgentFileChanged, err)
	}
	if !sameAgentFileMetadata(f.metadata, currentMetadata) {
		return fmt.Errorf("%w: destination metadata changed", errAgentFileChanged)
	}
	pathInfo, err := agentFilePathInfo(f.path)
	if err != nil || !sameAgentFileSnapshot(afterReadInfo, pathInfo) {
		return fmt.Errorf("%w: destination changed during verification", errAgentFileChanged)
	}
	return f.verifyRequestedPath(afterReadInfo)
}

func (f *lockedAgentFile) verifyRequestedPath(targetInfo os.FileInfo) error {
	if f == nil || f.requestedPath == "" {
		return fmt.Errorf("%w: requested path is unavailable", errAgentFileChanged)
	}
	if f.requestedInfo == nil {
		return fmt.Errorf("%w: initial requested-path identity is unavailable", errAgentFileChanged)
	}
	currentRequestedInfo, err := agentFilePathInfo(f.requestedPath)
	if err != nil {
		return fmt.Errorf("%w: stat requested path: %v", errAgentFileChanged, err)
	}
	if currentRequestedInfo.Mode()&os.ModeSymlink != 0 ||
		!currentRequestedInfo.Mode().IsRegular() ||
		!os.SameFile(f.requestedInfo, currentRequestedInfo) ||
		!os.SameFile(targetInfo, currentRequestedInfo) {
		return fmt.Errorf("%w: requested regular file changed", errAgentFileChanged)
	}
	return nil
}

func sameAgentFileSnapshot(a, b os.FileInfo) bool {
	return a != nil && b != nil &&
		os.SameFile(a, b) &&
		a.Size() == b.Size() &&
		a.Mode() == b.Mode() &&
		a.ModTime().Equal(b.ModTime())
}

// readAgentFileExactly bounds allocation before reading and detects growth or
// truncation while the caller holds its file descriptor.
func readAgentFileExactly(file *os.File, expectedSize int64) ([]byte, error) {
	if file == nil || expectedSize < 0 {
		return nil, fmt.Errorf("%w: invalid agent-file handle or size", errAgentFileChanged)
	}
	if expectedSize > maxAgentFileBytes {
		return nil, fmt.Errorf("%w: %d bytes exceeds %d", errAgentFileTooLarge, expectedSize, maxAgentFileBytes)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	content, err := io.ReadAll(&io.LimitedReader{R: file, N: expectedSize + 1})
	if err != nil {
		return nil, err
	}
	if int64(len(content)) != expectedSize {
		return nil, fmt.Errorf("%w: read %d bytes, expected %d", errAgentFileChanged, len(content), expectedSize)
	}
	return content, nil
}

func (f *lockedAgentFile) replace(content []byte) error {
	return writeVerifiedReplacement(f, content)
}

// AppendBlurbToFile appends the agent blurb to the specified file.
// The complete result is validated before a same-directory replacement so a
// blurb cannot be written inside an EOF-terminated Markdown fence.
func AppendBlurbToFile(filePath string) (returnErr error) {
	locked, err := lockAgentFileForMutation(filePath)
	if err != nil {
		return fmt.Errorf("read file: %w", err)
	}
	defer func() {
		returnErr = errors.Join(returnErr, locked.close())
	}()

	contentStr := string(locked.content)
	if _, err := inspectBlurbStructure(contentStr); err != nil {
		return fmt.Errorf("validate existing blurb markers: %w", err)
	}
	if ContainsAnyBlurb(contentStr) {
		return fmt.Errorf("agent file already contains bv instructions; update or remove them instead")
	}

	// Append blurb using the string function
	newContent := AppendBlurb(contentStr)
	count, err := inspectBlurbStructure(newContent)
	if err != nil {
		return fmt.Errorf("validate appended blurb: %w", err)
	}
	if count != 1 || GetBlurbVersion(newContent) != BlurbVersion {
		return fmt.Errorf("validate appended blurb: found %d standalone versioned blocks at v%d, want exactly one v%d block", count, GetBlurbVersion(newContent), BlurbVersion)
	}

	if err := locked.replace([]byte(newContent)); err != nil {
		return fmt.Errorf("write file: %w", err)
	}

	return nil
}

// UpdateBlurbInFile replaces an existing blurb with the current version.
// Uses a fully written same-directory replacement to prevent partial writes.
func UpdateBlurbInFile(filePath string) (returnErr error) {
	locked, err := lockAgentFileForMutation(filePath)
	if err != nil {
		return fmt.Errorf("read file: %w", err)
	}
	defer func() {
		returnErr = errors.Join(returnErr, locked.close())
	}()

	newContent, err := updateBlurbChecked(string(locked.content))
	if err != nil {
		return fmt.Errorf("validate existing blurb: %w", err)
	}

	if err := locked.replace([]byte(newContent)); err != nil {
		return fmt.Errorf("write file: %w", err)
	}

	return nil
}

// RemoveBlurbFromFile removes all versioned and legacy agent blurbs from the
// specified file. Malformed and future-version markers are rejected without
// writing. Uses a fully written same-directory replacement.
func RemoveBlurbFromFile(filePath string) (returnErr error) {
	locked, err := lockAgentFileForMutation(filePath)
	if err != nil {
		return fmt.Errorf("read file: %w", err)
	}
	defer func() {
		returnErr = errors.Join(returnErr, locked.close())
	}()

	newContent, err := removeBlurbsChecked(string(locked.content))
	if err != nil {
		return fmt.Errorf("validate existing blurb: %w", err)
	}
	if newContent == string(locked.content) {
		return nil
	}

	if err := locked.replace([]byte(newContent)); err != nil {
		return fmt.Errorf("write file: %w", err)
	}

	return nil
}

// CreateAgentFile creates a new AGENTS.md file with the blurb content.
// The file is created exclusively with standard permissions (0644); an
// existing path is never replaced, including if it appears after detection.
func CreateAgentFile(filePath string) error {
	content := "# AI Agent Instructions\n\n" + AgentBlurb + "\n"
	if err := writeNewFileExclusive(filePath, []byte(content)); err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	return nil
}

// VerifyBlurbPresent checks that exactly one structurally valid versioned blurb
// is present and that no legacy blurb remains.
func VerifyBlurbPresent(filePath string) (bool, error) {
	pathInfo, err := agentFilePathInfo(filePath)
	if err != nil {
		return false, err
	}
	if !pathInfo.Mode().IsRegular() {
		return false, fmt.Errorf("refusing to inspect non-regular agent file %q", filePath)
	}
	file, err := openAgentFileForInspection(filePath)
	if err != nil {
		return false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || !sameAgentFileSnapshot(pathInfo, info) {
		return false, errAgentFileChanged
	}
	content, err := readAgentFileExactly(file, info.Size())
	if err != nil {
		return false, err
	}
	afterInfo, err := file.Stat()
	if err != nil || !sameAgentFileSnapshot(info, afterInfo) {
		return false, errAgentFileChanged
	}
	currentInfo, err := agentFilePathInfo(filePath)
	if err != nil || !sameAgentFileSnapshot(afterInfo, currentInfo) {
		return false, errAgentFileChanged
	}
	contentStr := string(content)
	count, err := inspectBlurbStructure(contentStr)
	if err != nil {
		return false, fmt.Errorf("validate blurb structure: %w", err)
	}
	if count == 0 {
		return false, nil
	}
	if count > 1 {
		return false, fmt.Errorf("validate blurb structure: found %d versioned blurb blocks, want exactly 1", count)
	}
	if ContainsLegacyBlurb(contentStr) {
		return false, fmt.Errorf("validate blurb structure: legacy blurb remains alongside versioned blurb")
	}
	version := GetBlurbVersion(contentStr)
	if version != BlurbVersion {
		return false, fmt.Errorf("validate blurb version: found v%d, want current v%d", version, BlurbVersion)
	}
	return true, nil
}

func writeNewFileExclusive(filePath string, content []byte) error {
	// Build and verify the complete inode under a same-directory private name,
	// then use the platform's atomic no-replace rename primitive. Readers either
	// see no destination or the complete file; an existing destination is never
	// overwritten.
	return writeFileDirectExclusive(filePath, content)
}

// writeNewFileExclusiveUsing tries a hard-link publication first (one-shot
// no-replace create on filesystems that support it). os.ErrExist is returned
// unchanged. Any other link failure falls back to writeFileDirectExclusive so
// exFAT/FAT32 and injectable test failures keep the same no-replacement
// guarantee. link is injectable so tests can force the fallback path.
func writeNewFileExclusiveUsing(filePath string, content []byte, link func(string, string) error) error {
	if len(content) > maxAgentFileBytes {
		return fmt.Errorf("%w: %d bytes exceeds %d", errAgentFileTooLarge, len(content), maxAgentFileBytes)
	}
	dir := filepath.Dir(filePath)
	tmp, err := os.CreateTemp(dir, ".bv-create-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	var createdInfo os.FileInfo
	closed := false
	linked := false
	defer func() {
		if !closed {
			_ = tmp.Close()
		}
		if linked {
			_ = os.Remove(tmpPath)
			return
		}
		removeAgentReplacementIfSame(tmpPath, createdInfo)
	}()
	if info, statErr := tmp.Stat(); statErr == nil {
		createdInfo = info
	}
	if _, err := tmp.Write(content); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	closed = true
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err := link(tmpPath, filePath); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("link new file: %w", err)
		}
		if fallbackErr := writeFileDirectExclusive(filePath, content); fallbackErr != nil {
			return fmt.Errorf("link new file: %v; exclusive create fallback: %w", err, fallbackErr)
		}
		return nil
	}
	linked = true
	if err := syncAgentParentDirectory(filePath); err != nil {
		return fmt.Errorf("sync destination directory: %w", err)
	}
	return nil
}

func writeFileDirectExclusive(filePath string, content []byte) error {
	if len(content) > maxAgentFileBytes {
		return fmt.Errorf("%w: %d bytes exceeds %d", errAgentFileTooLarge, len(content), maxAgentFileBytes)
	}
	file, err := os.CreateTemp(filepath.Dir(filePath), ".bv-create-*")
	if err != nil {
		return fmt.Errorf("create private destination: %w", err)
	}
	tempPath := file.Name()
	var createdInfo os.FileInfo
	closed := false
	published := false
	defer func() {
		if !closed {
			_ = file.Close()
		}
		if published {
			return
		}
		removeAgentReplacementIfSame(tempPath, createdInfo)
	}()
	openedInfo, statErr := file.Stat()
	if statErr != nil {
		// Without a handle-derived identity it is unsafe to unlink this name:
		// another process may already have replaced it. Leave the artifact for
		// manual recovery rather than deleting an untrusted pathname.
		return fmt.Errorf("stat private destination: %w", statErr)
	}
	createdInfo = openedInfo
	pathInfo, pathStatErr := agentFilePathInfo(tempPath)
	if pathStatErr != nil {
		return fmt.Errorf("stat private destination path: %w", pathStatErr)
	}
	if !pathInfo.Mode().IsRegular() || !os.SameFile(openedInfo, pathInfo) {
		return fmt.Errorf("private destination path changed during creation")
	}

	written, err := file.Write(content)
	if err != nil {
		return fmt.Errorf("write private destination: %w", err)
	}
	if written != len(content) {
		return fmt.Errorf("write private destination: %w", io.ErrShortWrite)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync private destination: %w", err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("seek completed private destination: %w", err)
	}
	completed, err := io.ReadAll(file)
	if err != nil {
		return fmt.Errorf("verify completed private destination bytes: %w", err)
	}
	if !bytes.Equal(content, completed) {
		return fmt.Errorf("private destination bytes changed while the exclusive create was being completed")
	}
	// Keep the unpublished candidate private until its complete bytes have been
	// synced and verified; only then apply the destination's public mode.
	if err := file.Chmod(0o644); err != nil {
		return fmt.Errorf("chmod private destination: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync private destination metadata: %w", err)
	}
	finalInfo, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat completed private destination: %w", err)
	}
	pathInfo, err = agentFilePathInfo(tempPath)
	if err != nil || !pathInfo.Mode().IsRegular() || !os.SameFile(finalInfo, pathInfo) {
		return fmt.Errorf("private destination path changed while the exclusive create was being completed")
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close destination: %w", err)
	}
	closed = true
	pathInfo, err = agentFilePathInfo(tempPath)
	if err != nil || !pathInfo.Mode().IsRegular() || !os.SameFile(finalInfo, pathInfo) {
		return fmt.Errorf("private destination path changed after the exclusive create was closed")
	}
	if err := publishAgentFileExclusive(tempPath, filePath); err != nil {
		return fmt.Errorf("publish destination exclusively: %w", err)
	}
	published = true
	pathInfo, err = agentFilePathInfo(filePath)
	if err != nil || !pathInfo.Mode().IsRegular() || !os.SameFile(finalInfo, pathInfo) {
		return fmt.Errorf("destination path changed during exclusive publication")
	}
	if err := syncAgentParentDirectory(filePath); err != nil {
		return fmt.Errorf("sync destination directory: %w", err)
	}
	return nil
}

// writeVerifiedReplacement writes and syncs a complete same-directory temp
// file, copies the source's platform metadata, verifies the locked source, and
// atomically commits through the platform replacement primitive. Cooperating
// bv writers stay serialized for the entire operation. A process that ignores
// that coordination can still race the final pathname operation, so the
// verification is deliberately described as best-effort rather than a
// portable compare-and-swap guarantee.
func writeVerifiedReplacement(locked *lockedAgentFile, content []byte) error {
	if locked == nil || locked.file == nil || locked.unlock == nil {
		return fmt.Errorf("replacement requires a live locked source")
	}
	if len(content) > maxAgentFileBytes {
		return fmt.Errorf("%w: %d bytes exceeds %d", errAgentFileTooLarge, len(content), maxAgentFileBytes)
	}
	// Create the replacement in the same directory so the commit stays on one
	// filesystem. Platform implementations establish a handle-derived identity
	// and a security policy no broader than the source before any bytes are
	// written to the candidate.
	tmp, tmpPath, createdInfo, err := createAgentReplacementFile(locked)
	if err != nil {
		return fmt.Errorf("create replacement file: %w", err)
	}

	success := false
	cleanupAllowed := true
	defer func() {
		if !success {
			if cleanupAllowed {
				_ = makeAgentReplacementRemovable(tmp)
				_ = tmp.Close()
				removeAgentReplacementIfSame(tmpPath, createdInfo)
				return
			}
		}
		_ = tmp.Close()
	}()

	if err := verifyAgentReplacementPath(tmpPath, createdInfo); err != nil {
		return err
	}
	if err := tmp.Truncate(0); err != nil {
		return fmt.Errorf("truncate replacement file: %w", err)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("seek replacement file: %w", err)
	}
	written, err := tmp.Write(content)
	if err != nil {
		return fmt.Errorf("write replacement file: %w", err)
	}
	if written != len(content) {
		return fmt.Errorf("write replacement file: %w", io.ErrShortWrite)
	}

	// Flush the completed bytes before metadata preparation.
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync temp file: %w", err)
	}

	// Check before reading security metadata as well as again inside the commit.
	// A displaced Unix inode reports a zero link count and should be diagnosed
	// as a stale destination, not as an originally hard-linked source.
	if err := locked.verifyUnchanged(); err != nil {
		return fmt.Errorf("verify destination before metadata copy: %w", err)
	}
	if err := prepareAgentReplacementMetadata(locked.file, tmp, locked.info.Mode()); err != nil {
		return fmt.Errorf("preserve destination metadata: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync temp metadata: %w", err)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("seek completed replacement file: %w", err)
	}
	writtenContent, err := io.ReadAll(tmp)
	if err != nil {
		return fmt.Errorf("verify completed replacement bytes: %w", err)
	}
	if !bytes.Equal(content, writtenContent) {
		return fmt.Errorf("replacement bytes changed while the bv edit was being prepared")
	}
	finalInfo, err := tmp.Stat()
	if err != nil {
		return fmt.Errorf("stat completed replacement file: %w", err)
	}
	if err := verifyAgentReplacementPath(tmpPath, finalInfo); err != nil {
		return err
	}
	if err := locked.verifyUnchanged(); err != nil {
		return fmt.Errorf("verify destination before replacement: %w", err)
	}

	cleanupAllowed, err = commitAgentReplacement(locked, tmp, tmpPath, finalInfo)
	if err != nil {
		return err
	}
	cleanupAllowed = false
	if err := syncAgentParentDirectory(locked.path); err != nil {
		return fmt.Errorf("sync replacement directory: %w", err)
	}

	success = true
	return nil
}

func verifyAgentReplacementPath(path string, expected os.FileInfo) error {
	pathInfo, err := agentFilePathInfo(path)
	if err != nil {
		return fmt.Errorf("replacement path changed: %w", err)
	}
	if !pathInfo.Mode().IsRegular() || !sameAgentFileSnapshot(expected, pathInfo) {
		return fmt.Errorf("replacement path changed while the bv edit was being prepared")
	}
	return nil
}

func removeAgentReplacementIfSame(path string, expected os.FileInfo) {
	pathInfo, err := agentFilePathInfo(path)
	if err == nil && expected != nil && pathInfo.Mode().IsRegular() && os.SameFile(expected, pathInfo) {
		_ = os.Remove(path)
	}
}

// EnsureBlurb ensures the blurb is present in an agent file.
// If the file exists without blurb, appends it.
// If the file has an old version, updates it.
// If the file doesn't exist, creates it.
func EnsureBlurb(workDir string) error {
	detection := DetectAgentFile(workDir)

	if !detection.Found() {
		// No agent file exists - create one
		filePath := GetPreferredAgentFilePath(workDir)
		return CreateAgentFile(filePath)
	}

	if detection.NeedsBlurb() {
		// File exists but no blurb - append
		return AppendBlurbToFile(detection.FilePath)
	}

	if detection.NeedsUpgrade() {
		// File has old blurb - update
		return UpdateBlurbInFile(detection.FilePath)
	}

	// Already has current blurb
	return nil
}
