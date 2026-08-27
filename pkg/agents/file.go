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
	errAgentFileBusy                 = errors.New("agent file is busy with another bv edit")
	errAgentFileChanged              = errors.New("agent file changed while the bv edit was being prepared")
	errAgentFileTooLarge             = errors.New("agent file exceeds the safe mutation size limit")
	openAgentFileForInspectionAtPath = openAgentFileForInspection
	closeAgentInspectionFile         = func(file *os.File) error { return file.Close() }
	closeAgentCreateFile             = func(file *os.File) error { return file.Close() }
	closeAgentReplacementFile        = func(file *os.File) error { return file.Close() }
	beforeAgentReplacementFirstWrite = func(*os.File) error { return nil }
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
		beforeMetadata, err := snapshotAgentFileMetadata(file)
		if err != nil {
			closeLocked()
			return nil, fmt.Errorf("snapshot agent-file metadata before read: %w", err)
		}
		content, readErr := readAgentFileExactly(file, beforeInfo.Size())
		afterInfo, afterStatErr := file.Stat()
		pathInfo, pathStatErr = agentFilePathInfo(mutationPath)
		afterMetadata, afterMetadataErr := snapshotAgentFileMetadata(file)
		if readErr != nil || afterStatErr != nil || pathStatErr != nil || afterMetadataErr != nil {
			closeLocked()
			if readErr != nil {
				return nil, readErr
			}
			if afterStatErr != nil {
				return nil, afterStatErr
			}
			if pathStatErr != nil {
				return nil, pathStatErr
			}
			return nil, fmt.Errorf("snapshot agent-file metadata after read: %w", afterMetadataErr)
		}
		if !sameAgentFileSnapshot(beforeInfo, afterInfo) || int64(len(content)) != afterInfo.Size() ||
			!pathInfo.Mode().IsRegular() || !os.SameFile(afterInfo, pathInfo) ||
			!sameAgentFileMetadata(beforeMetadata, afterMetadata) {
			closeLocked()
			return nil, errAgentFileChanged
		}

		locked := &lockedAgentFile{
			requestedPath: requestedPath,
			requestedInfo: requestedInfo,
			path:          mutationPath,
			file:          file,
			unlock:        unlock,
			info:          afterInfo,
			metadata:      afterMetadata,
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
	if !sameAgentFileSnapshot(f.info, currentInfo) {
		return fmt.Errorf("%w: locked source snapshot changed", errAgentFileChanged)
	}
	currentMetadata, err := snapshotAgentFileMetadata(f.file)
	if err != nil {
		return fmt.Errorf("%w: snapshot current metadata: %v", errAgentFileChanged, err)
	}
	if !sameAgentFileMetadata(f.metadata, currentMetadata) {
		return fmt.Errorf("%w: destination metadata changed", errAgentFileChanged)
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
	afterReadMetadata, err := snapshotAgentFileMetadata(f.file)
	if err != nil {
		return fmt.Errorf("%w: snapshot current metadata: %v", errAgentFileChanged, err)
	}
	if !sameAgentFileMetadata(currentMetadata, afterReadMetadata) {
		return fmt.Errorf("%w: destination metadata changed while it was being verified", errAgentFileChanged)
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

func readAgentFileExactly(file *os.File, expectedSize int64) ([]byte, error) {
	if file == nil {
		return nil, fmt.Errorf("agent-file handle is nil")
	}
	if expectedSize < 0 {
		return nil, fmt.Errorf("invalid negative agent-file size %d", expectedSize)
	}
	if expectedSize > maxAgentFileBytes {
		return nil, fmt.Errorf("%w: %d bytes exceeds %d", errAgentFileTooLarge, expectedSize, maxAgentFileBytes)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	reader := &io.LimitedReader{R: file, N: expectedSize + 1}
	content, err := io.ReadAll(reader)
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
func VerifyBlurbPresent(filePath string) (present bool, returnErr error) {
	file, err := openAgentFileForInspectionAtPath(filePath)
	if err != nil {
		return false, err
	}
	defer func() {
		if closeErr := closeAgentInspectionFile(file); closeErr != nil {
			present = false
			returnErr = errors.Join(returnErr, fmt.Errorf("close agent-file inspection handle: %w", closeErr))
		}
	}()
	info, err := file.Stat()
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("refusing to inspect non-regular agent file %q", filePath)
	}
	beforeMetadata, err := snapshotAgentFileMetadata(file)
	if err != nil {
		return false, fmt.Errorf("snapshot agent-file metadata before inspection: %w", err)
	}
	content, err := readAgentFileExactly(file, info.Size())
	if err != nil {
		return false, err
	}
	afterInfo, err := file.Stat()
	if err != nil {
		return false, err
	}
	afterMetadata, err := snapshotAgentFileMetadata(file)
	if err != nil {
		return false, fmt.Errorf("snapshot agent-file metadata after inspection: %w", err)
	}
	pathInfo, err := agentFilePathInfo(filePath)
	if err != nil || !sameAgentFileSnapshot(info, afterInfo) ||
		!sameAgentFileMetadata(beforeMetadata, afterMetadata) ||
		!sameAgentFileSnapshot(afterInfo, pathInfo) {
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

var publishAgentFileExclusiveForCreate = publishAgentFileExclusive

func writeFileDirectExclusive(filePath string, content []byte) (returnErr error) {
	if int64(len(content)) > maxAgentFileBytes {
		return fmt.Errorf("%w: new file is %d bytes, maximum is %d", errAgentFileTooLarge, len(content), maxAgentFileBytes)
	}
	// Avoid manufacturing a complete recovery artifact for the ordinary case
	// where the caller is retrying creation of a path that already exists. The
	// final publication remains no-replace, so an object that appears after this
	// advisory check is still handled safely by publishAgentFileExclusive.
	if _, err := agentFilePathInfo(filePath); err == nil {
		return fmt.Errorf("destination %q already exists: %w", filePath, os.ErrExist)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect destination before exclusive create: %w", err)
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
			closeErr := closeAgentCreateFile(file)
			closed = true
			if closeErr != nil {
				returnErr = errors.Join(returnErr, fmt.Errorf("close unpublished destination handle: %w", closeErr))
			}
		}
		if published {
			return
		}
		if returnErr != nil {
			pathInfo, statErr := agentFilePathInfo(tempPath)
			if statErr == nil && createdInfo != nil && pathInfo.Mode().IsRegular() && os.SameFile(createdInfo, pathInfo) {
				returnErr = fmt.Errorf("%w; unpublished file retained at %q for recovery", returnErr, tempPath)
			}
		}
		removeAgentReplacementIfSame(tempPath, createdInfo)
	}()
	openedInfo, statErr := file.Stat()
	if statErr != nil {
		// Without a handle-derived identity it is unsafe to unlink this name:
		// another process may already have replaced it. Leave the artifact for
		// manual recovery rather than deleting an untrusted pathname.
		return fmt.Errorf("stat private destination at %q: %w", tempPath, statErr)
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
	completedBeforeInfo, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat completed private destination before read: %w", err)
	}
	pathInfo, err = agentFilePathInfo(tempPath)
	if err != nil || !pathInfo.Mode().IsRegular() || !sameAgentFileSnapshot(completedBeforeInfo, pathInfo) {
		return fmt.Errorf("private destination path changed before completed bytes were verified")
	}
	completed, err := readAgentFileExactly(file, int64(len(content)))
	if err != nil {
		return fmt.Errorf("verify completed private destination bytes: %w", err)
	}
	if !bytes.Equal(content, completed) {
		return fmt.Errorf("private destination bytes changed while the exclusive create was being completed")
	}
	completedAfterInfo, err := file.Stat()
	if err != nil || !sameAgentFileSnapshot(completedBeforeInfo, completedAfterInfo) {
		return fmt.Errorf("private destination changed while completed bytes were being verified")
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
	closeErr := closeAgentCreateFile(file)
	closed = true
	if closeErr != nil {
		return fmt.Errorf("close destination: %w", closeErr)
	}
	pathInfo, err = agentFilePathInfo(tempPath)
	if err != nil || !pathInfo.Mode().IsRegular() || !os.SameFile(finalInfo, pathInfo) {
		return fmt.Errorf("private destination path changed after the exclusive create was closed")
	}
	partialPublicationError := func(operationErr error) error {
		return fmt.Errorf("destination was published at %q; publication is partial: %w", filePath, operationErr)
	}
	didPublish, publishErr := publishAgentFileExclusiveForCreate(tempPath, filePath)
	published = didPublish
	if publishErr != nil {
		if published {
			return partialPublicationError(fmt.Errorf("publish destination exclusively while retaining private source name %q: %w", tempPath, publishErr))
		}
		return fmt.Errorf("publish destination exclusively: %w", publishErr)
	}
	if !published {
		return fmt.Errorf("publish destination exclusively: platform returned no error without publishing")
	}
	pathInfo, err = agentFilePathInfo(filePath)
	if err != nil {
		return partialPublicationError(fmt.Errorf("stat destination after exclusive publication: %w", err))
	}
	if !pathInfo.Mode().IsRegular() || !os.SameFile(finalInfo, pathInfo) {
		return partialPublicationError(fmt.Errorf("destination path changed during exclusive publication"))
	}
	publishedFile, err := openAgentFileForInspectionAtPath(filePath)
	if err != nil {
		return partialPublicationError(fmt.Errorf("open published destination for verification: %w", err))
	}
	beforeLinkErr := verifyAgentFileHasSingleLink(publishedFile)
	publishedContent, readErr := readAgentFileExactly(publishedFile, int64(len(content)))
	publishedInfo, statErr := publishedFile.Stat()
	afterLinkErr := verifyAgentFileHasSingleLink(publishedFile)
	closeErr = closeAgentInspectionFile(publishedFile)
	if closeErr != nil {
		closeErr = fmt.Errorf("close published destination inspection handle: %w", closeErr)
	}
	pathInfo, pathStatErr = agentFilePathInfo(filePath)
	verificationErr := errors.Join(beforeLinkErr, readErr, statErr, afterLinkErr, closeErr, pathStatErr)
	if !bytes.Equal(publishedContent, content) {
		verificationErr = errors.Join(verificationErr, fmt.Errorf("published destination bytes differ from intended content"))
	}
	if !sameAgentFileSnapshot(finalInfo, publishedInfo) {
		verificationErr = errors.Join(verificationErr, fmt.Errorf("published destination inode differs from the prepared file"))
	}
	if !sameAgentFileSnapshot(publishedInfo, pathInfo) {
		verificationErr = errors.Join(verificationErr, fmt.Errorf("published destination path changed during verification"))
	}
	if verificationErr != nil {
		return partialPublicationError(fmt.Errorf("verify published destination: %w", verificationErr))
	}
	if err := syncAgentParentDirectory(filePath); err != nil {
		return partialPublicationError(fmt.Errorf("sync destination directory: %w", err))
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
func writeVerifiedReplacement(locked *lockedAgentFile, content []byte) (returnErr error) {
	if locked == nil || locked.file == nil || locked.unlock == nil {
		return fmt.Errorf("replacement requires a live locked source")
	}
	if int64(len(content)) > maxAgentFileBytes {
		return fmt.Errorf("%w: replacement is %d bytes, maximum is %d", errAgentFileTooLarge, len(content), maxAgentFileBytes)
	}
	// Create the replacement in the same directory so the commit stays on one
	// filesystem. Platform implementations establish a handle-derived identity
	// and a security policy no broader than the source before any bytes are
	// written to the candidate.
	tmp, tmpPath, createdInfo, err := createAgentReplacementFile(locked)
	if err != nil {
		return fmt.Errorf("create replacement file: %w", err)
	}
	publicationOccurred := false

	defer func() {
		if returnErr != nil {
			if !publicationOccurred {
				if privacyErr := makeAgentReplacementPrivateAfterFailure(tmp); privacyErr != nil {
					returnErr = fmt.Errorf("%w; failed to resecure unpublished replacement: %v", returnErr, privacyErr)
				}
			}
			pathInfo, statErr := agentFilePathInfo(tmpPath)
			if statErr == nil && createdInfo != nil && pathInfo.Mode().IsRegular() && os.SameFile(createdInfo, pathInfo) {
				returnErr = fmt.Errorf("%w; replacement retained at %q for recovery", returnErr, tmpPath)
			}
		}
		if closeErr := closeAgentReplacementFile(tmp); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
			// ReplaceFileW requires the candidate handle to be closed before
			// publication. Its platform commit therefore closes tmp deliberately;
			// the shared cleanup remains idempotent without hiding any other close
			// failure (including injected failures in the causal tests).
			if publicationOccurred {
				returnErr = errors.Join(
					returnErr,
					fmt.Errorf("replacement was published, but closing its open handle failed; publication is partial: %w", closeErr),
				)
			} else {
				returnErr = errors.Join(returnErr, fmt.Errorf("close unpublished replacement handle: %w", closeErr))
			}
		}
	}()

	if err := verifyAgentReplacementPath(tmpPath, createdInfo); err != nil {
		return err
	}
	if err := beforeAgentReplacementFirstWrite(tmp); err != nil {
		return fmt.Errorf("verify replacement allocation metadata before first write: %w", err)
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
	preparedInfo, err := tmp.Stat()
	if err != nil {
		return fmt.Errorf("stat prepared replacement file: %w", err)
	}
	preparedMetadata, err := snapshotAgentFileMetadata(tmp)
	if err != nil {
		return fmt.Errorf("snapshot prepared replacement metadata: %w", err)
	}
	if err := verifyAgentReplacementPath(tmpPath, preparedInfo); err != nil {
		return err
	}
	writtenContent, err := readAgentFileExactly(tmp, int64(len(content)))
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
	finalMetadata, err := snapshotAgentFileMetadata(tmp)
	if err != nil {
		return fmt.Errorf("snapshot completed replacement metadata: %w", err)
	}
	if !sameAgentFileSnapshot(preparedInfo, finalInfo) || !sameAgentFileMetadata(preparedMetadata, finalMetadata) {
		return fmt.Errorf("replacement file or metadata changed while its bytes were being verified")
	}
	if err := verifyAgentReplacementPath(tmpPath, finalInfo); err != nil {
		return err
	}
	if err := locked.verifyUnchanged(); err != nil {
		return fmt.Errorf("verify destination before replacement: %w", err)
	}
	// Apply the source's access-enabling mode only after the complete private
	// candidate has passed byte and metadata stability checks. Any failure from
	// here until publication re-secures the unpublished inode through its handle.
	if err := finalizeAgentReplacementAccess(locked.file, tmp, locked.info.Mode()); err != nil {
		return fmt.Errorf("finalize destination access metadata: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync finalized replacement metadata: %w", err)
	}
	publishInfo, err := tmp.Stat()
	if err != nil {
		return fmt.Errorf("stat finalized replacement file: %w", err)
	}
	if err := verifyAgentReplacementPath(tmpPath, publishInfo); err != nil {
		return err
	}

	publicationOccurred, err = commitAgentReplacement(locked, tmp, tmpPath, publishInfo, content)
	if err != nil {
		return err
	}
	if !publicationOccurred {
		if err := verifyPublishedAgentReplacement(locked.path, publishInfo, content); err != nil {
			return fmt.Errorf("verify published replacement: %w", err)
		}
		if err := syncAgentParentDirectory(locked.path); err != nil {
			return fmt.Errorf("sync replacement directory: %w", err)
		}
	}

	return nil
}

func verifyPublishedAgentReplacement(path string, expectedInfo os.FileInfo, expectedContent []byte) (returnErr error) {
	file, err := openAgentFileForInspectionAtPath(path)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := closeAgentInspectionFile(file); closeErr != nil {
			returnErr = errors.Join(
				returnErr,
				fmt.Errorf("published replacement inspection handle failed to close; publication is partial: %w", closeErr),
			)
		}
	}()
	beforeInfo, err := file.Stat()
	if err != nil {
		return err
	}
	if !beforeInfo.Mode().IsRegular() || !sameAgentFileSnapshot(expectedInfo, beforeInfo) {
		return fmt.Errorf("published path does not identify the prepared replacement")
	}
	beforeMetadata, err := snapshotAgentFileMetadata(file)
	if err != nil {
		return fmt.Errorf("snapshot published metadata: %w", err)
	}
	if err := verifyAgentFileHasSingleLink(file); err != nil {
		return err
	}
	content, err := readAgentFileExactly(file, int64(len(expectedContent)))
	if err != nil {
		return err
	}
	afterInfo, err := file.Stat()
	if err != nil {
		return err
	}
	afterMetadata, err := snapshotAgentFileMetadata(file)
	if err != nil {
		return fmt.Errorf("resnapshot published metadata: %w", err)
	}
	if err := verifyAgentFileHasSingleLink(file); err != nil {
		return err
	}
	pathInfo, err := agentFilePathInfo(path)
	if err != nil {
		return err
	}
	if !bytes.Equal(content, expectedContent) ||
		!sameAgentFileSnapshot(beforeInfo, afterInfo) ||
		!sameAgentFileMetadata(beforeMetadata, afterMetadata) ||
		!sameAgentFileSnapshot(afterInfo, pathInfo) {
		return fmt.Errorf("published replacement changed during final verification")
	}
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
	// There is no portable unlink-by-handle primitive. Checking identity and
	// then removing this pathname would leave a race in which a peer replaces
	// the name after the check and has its file deleted by bv. Retain failed
	// candidates for explicit recovery instead of risking peer data loss.
	_, _ = path, expected
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
	if detection.HasMalformedBlurb() {
		return fmt.Errorf("refusing to modify malformed bv agent blurb: %s", detection.BlurbStructureError)
	}
	if detection.HasFutureBlurb() {
		return fmt.Errorf("refusing to downgrade bv agent blurb v%d with binary supporting v%d", detection.BlurbVersion, BlurbVersion)
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
