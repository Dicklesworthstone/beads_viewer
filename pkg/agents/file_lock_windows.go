//go:build windows

package agents

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var replaceFileW = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReplaceFileW")

var replaceAgentFile = func(destination, replacement *uint16) error {
	result, _, callErr := replaceFileW.Call(
		uintptr(unsafe.Pointer(destination)),
		uintptr(unsafe.Pointer(replacement)),
		0,
		0,
		0,
		0,
	)
	if result != 0 {
		return nil
	}
	if callErr != nil && callErr != windows.ERROR_SUCCESS {
		return callErr
	}
	return fmt.Errorf("failed without a Windows error code")
}

type agentFileMetadataSnapshot struct {
	attributes       uint32
	volumeSerial     uint32
	linkCount        uint32
	fileIndexHigh    uint32
	fileIndexLow     uint32
	creationTimeHigh uint32
	creationTimeLow  uint32
	writeTimeHigh    uint32
	writeTimeLow     uint32
	security         [sha256.Size]byte
	mandatoryLabel   [sha256.Size]byte
}

// agentFilePathInfo implements Lstat-like final-component semantics while
// returning handle-derived volume/file IDs. Go's Windows os.Lstat defers
// loading those IDs until os.SameFile, whose internal reopen uses share mode
// zero. That reopen conflicts with the source or private replacement handles
// held by this package and makes a file appear to differ from itself. Opening
// with no requested data access and permissive sharing avoids that self-
// conflict without granting access that the path's DACL does not already
// permit; FILE_FLAG_OPEN_REPARSE_POINT preserves final-symlink detection.
func agentFilePathInfo(path string) (os.FileInfo, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		name,
		0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), path)
	info, statErr := file.Stat()
	closeErr := file.Close()
	if statErr != nil {
		return nil, statErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return info, nil
}

func openAgentFileForInspection(path string) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		name,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(handle), path), nil
}

func snapshotAgentFileMetadata(file *os.File) (agentFileMetadataSnapshot, error) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &info); err != nil {
		return agentFileMetadataSnapshot{}, err
	}
	security, err := windows.GetSecurityInfo(
		windows.Handle(file.Fd()),
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.GROUP_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return agentFileMetadataSnapshot{}, err
	}
	securityDigest, err := windowsSecurityDescriptorDigest(security)
	if err != nil {
		return agentFileMetadataSnapshot{}, fmt.Errorf("digest owner/group/DACL descriptor: %w", err)
	}
	_, labelDigest, err := windowsMandatoryLabel(file)
	if err != nil {
		return agentFileMetadataSnapshot{}, fmt.Errorf("read mandatory integrity label: %w", err)
	}
	return agentFileMetadataSnapshot{
		attributes:       info.FileAttributes,
		volumeSerial:     info.VolumeSerialNumber,
		linkCount:        info.NumberOfLinks,
		fileIndexHigh:    info.FileIndexHigh,
		fileIndexLow:     info.FileIndexLow,
		creationTimeHigh: info.CreationTime.HighDateTime,
		creationTimeLow:  info.CreationTime.LowDateTime,
		writeTimeHigh:    info.LastWriteTime.HighDateTime,
		writeTimeLow:     info.LastWriteTime.LowDateTime,
		security:         securityDigest,
		mandatoryLabel:   labelDigest,
	}, nil
}

func sameAgentFileMetadata(a, b agentFileMetadataSnapshot) bool {
	return a == b
}

func windowsSecurityDescriptorDigest(security *windows.SECURITY_DESCRIPTOR) ([sha256.Size]byte, error) {
	if security == nil || !security.IsValid() {
		return [sha256.Size]byte{}, fmt.Errorf("invalid security descriptor")
	}
	length := security.Length()
	if length == 0 || length > 1<<20 {
		return [sha256.Size]byte{}, fmt.Errorf("invalid security descriptor length %d", length)
	}
	raw := unsafe.Slice((*byte)(unsafe.Pointer(security)), int(length))
	digest := sha256.Sum256(raw)
	runtime.KeepAlive(security)
	return digest, nil
}

func windowsMandatoryLabel(file *os.File) (*windows.SECURITY_DESCRIPTOR, [sha256.Size]byte, error) {
	security, err := windows.GetSecurityInfo(
		windows.Handle(file.Fd()),
		windows.SE_FILE_OBJECT,
		windows.LABEL_SECURITY_INFORMATION,
	)
	if err != nil {
		return nil, [sha256.Size]byte{}, err
	}
	digest, err := windowsSecurityDescriptorDigest(security)
	if err != nil {
		return nil, [sha256.Size]byte{}, err
	}
	return security, digest, nil
}

func createAgentReplacementFile(locked *lockedAgentFile) (*os.File, string, os.FileInfo, error) {
	dir := filepath.Dir(locked.path)
	for attempt := 0; attempt < 64; attempt++ {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return nil, "", nil, fmt.Errorf("generate replacement name: %w", err)
		}
		path := filepath.Join(dir, ".bv-replace-"+hex.EncodeToString(random[:]))
		name, err := windows.UTF16PtrFromString(path)
		if err != nil {
			return nil, "", nil, err
		}
		handle, err := windows.CreateFile(
			name,
			windows.GENERIC_READ|windows.GENERIC_WRITE|windows.WRITE_DAC|windows.WRITE_OWNER,
			// Deny every peer open until the source DACL and integrity label have
			// been applied. Otherwise a principal permitted by the parent but
			// denied by the source could read rewritten bytes from the temp name.
			0,
			nil,
			windows.CREATE_NEW,
			windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_ATTRIBUTE_TEMPORARY,
			0,
		)
		if errors.Is(err, windows.ERROR_FILE_EXISTS) || errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			continue
		}
		if err != nil {
			return nil, "", nil, err
		}
		file := os.NewFile(uintptr(handle), path)
		openedInfo, err := file.Stat()
		if err != nil {
			_ = file.Close()
			// No trusted handle identity means the path cannot be cleaned safely.
			return nil, "", nil, err
		}
		pathInfo, err := agentFilePathInfo(path)
		if err != nil {
			_ = file.Close()
			removeAgentReplacementIfSame(path, openedInfo)
			return nil, "", nil, err
		}
		if !pathInfo.Mode().IsRegular() || !os.SameFile(openedInfo, pathInfo) {
			_ = file.Close()
			removeAgentReplacementIfSame(path, openedInfo)
			return nil, "", nil, fmt.Errorf("replacement path changed during creation")
		}
		return file, path, openedInfo, nil
	}
	return nil, "", nil, fmt.Errorf("could not allocate a unique replacement name")
}

func openAndLockAgentFileForMutation(path string, timeout time.Duration) (*os.File, func() error, error) {
	pathName, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, nil, err
	}
	handle, err := windows.CreateFile(
		pathName,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		return nil, nil, err
	}
	file := os.NewFile(uintptr(handle), path)
	var identity windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &identity); err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	name, err := windows.UTF16PtrFromString(fmt.Sprintf(
		`Global\bv-agent-file-%08x-%08x%08x`,
		identity.VolumeSerialNumber,
		identity.FileIndexHigh,
		identity.FileIndexLow,
	))
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}

	// Win32 mutex ownership belongs to an OS thread, not a Go goroutine. Keep
	// this goroutine pinned from acquisition through ReleaseMutex so migration
	// cannot leak the lock or accidentally exploit recursive thread ownership.
	runtime.LockOSThread()
	mutex, err := windows.CreateMutex(nil, false, name)
	if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		runtime.UnlockOSThread()
		_ = file.Close()
		return nil, nil, err
	}

	waitMillis := uint32((timeout + time.Millisecond - 1) / time.Millisecond)
	if waitMillis == 0 {
		waitMillis = 1
	}
	event, waitErr := windows.WaitForSingleObject(mutex, waitMillis)
	if waitErr != nil {
		_ = windows.CloseHandle(mutex)
		runtime.UnlockOSThread()
		_ = file.Close()
		return nil, nil, waitErr
	}
	if event == uint32(windows.WAIT_TIMEOUT) {
		_ = windows.CloseHandle(mutex)
		runtime.UnlockOSThread()
		_ = file.Close()
		return nil, nil, fmt.Errorf("%w after %s", errAgentFileBusy, timeout)
	}
	if event != windows.WAIT_OBJECT_0 && event != windows.WAIT_ABANDONED {
		_ = windows.CloseHandle(mutex)
		runtime.UnlockOSThread()
		_ = file.Close()
		return nil, nil, fmt.Errorf("wait for agent-file mutex returned %#x", event)
	}

	unlock := func() error {
		releaseErr := windows.ReleaseMutex(mutex)
		closeErr := windows.CloseHandle(mutex)
		runtime.UnlockOSThread()
		return errors.Join(releaseErr, closeErr)
	}
	return file, unlock, nil
}

func prepareAgentReplacementMetadata(source, replacement *os.File, _ os.FileMode) error {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(source.Fd()), &info); err != nil {
		return fmt.Errorf("stat source metadata: %w", err)
	}
	if info.NumberOfLinks != 1 {
		return fmt.Errorf("refusing to replace agent file with %d hard links", info.NumberOfLinks)
	}
	security, err := windows.GetSecurityInfo(
		windows.Handle(source.Fd()),
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.GROUP_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return fmt.Errorf("read source security descriptor: %w", err)
	}
	owner, _, err := security.Owner()
	if err != nil {
		return fmt.Errorf("read source owner: %w", err)
	}
	group, _, err := security.Group()
	if err != nil {
		return fmt.Errorf("read source group: %w", err)
	}
	dacl, _, err := security.DACL()
	if err != nil {
		return fmt.Errorf("read source DACL: %w", err)
	}
	replacementSecurity, err := windows.GetSecurityInfo(
		windows.Handle(replacement.Fd()),
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.GROUP_SECURITY_INFORMATION,
	)
	if err != nil {
		return fmt.Errorf("read replacement owner/group: %w", err)
	}
	replacementOwner, _, err := replacementSecurity.Owner()
	if err != nil {
		return fmt.Errorf("read replacement owner: %w", err)
	}
	replacementGroup, _, err := replacementSecurity.Group()
	if err != nil {
		return fmt.Errorf("read replacement group: %w", err)
	}
	securityInfo := windows.SECURITY_INFORMATION(windows.DACL_SECURITY_INFORMATION)
	var ownerToSet, groupToSet *windows.SID
	if owner.String() != replacementOwner.String() {
		securityInfo |= windows.OWNER_SECURITY_INFORMATION
		ownerToSet = owner
	}
	if group.String() != replacementGroup.String() {
		securityInfo |= windows.GROUP_SECURITY_INFORMATION
		groupToSet = group
	}
	control, _, err := security.Control()
	if err != nil {
		return fmt.Errorf("read source DACL control: %w", err)
	}
	if control&windows.SE_DACL_PROTECTED != 0 {
		securityInfo |= windows.PROTECTED_DACL_SECURITY_INFORMATION
	} else {
		securityInfo |= windows.UNPROTECTED_DACL_SECURITY_INFORMATION
	}
	if err := windows.SetSecurityInfo(
		windows.Handle(replacement.Fd()),
		windows.SE_FILE_OBJECT,
		securityInfo,
		ownerToSet,
		groupToSet,
		dacl,
		nil,
	); err != nil {
		return fmt.Errorf("copy source owner/group/DACL: %w", err)
	}

	// Mandatory integrity labels live in the SACL, but unlike audit SACLs they
	// are queryable with READ_CONTROL and settable with WRITE_OWNER. Preserve
	// this independently so a replacement cannot silently raise or lower the
	// destination's integrity policy.
	sourceLabelSecurity, sourceLabelDigest, err := windowsMandatoryLabel(source)
	if err != nil {
		return fmt.Errorf("read source mandatory integrity label: %w", err)
	}
	_, replacementLabelDigest, err := windowsMandatoryLabel(replacement)
	if err != nil {
		return fmt.Errorf("read replacement mandatory integrity label: %w", err)
	}
	if sourceLabelDigest != replacementLabelDigest {
		labelACL, _, labelErr := sourceLabelSecurity.SACL()
		if labelErr != nil && !errors.Is(labelErr, windows.ERROR_OBJECT_NOT_FOUND) {
			return fmt.Errorf("read source mandatory integrity label ACL: %w", labelErr)
		}
		if err := windows.SetSecurityInfo(
			windows.Handle(replacement.Fd()),
			windows.SE_FILE_OBJECT,
			windows.LABEL_SECURITY_INFORMATION,
			nil,
			nil,
			nil,
			labelACL,
		); err != nil {
			return fmt.Errorf("copy source mandatory integrity label: %w", err)
		}
		runtime.KeepAlive(sourceLabelSecurity)
		_, replacementLabelDigest, err = windowsMandatoryLabel(replacement)
		if err != nil {
			return fmt.Errorf("verify replacement mandatory integrity label: %w", err)
		}
		if sourceLabelDigest != replacementLabelDigest {
			return fmt.Errorf("replacement mandatory integrity label differs from source")
		}
	}
	// ReplaceFileW additionally merges destination attributes, encryption and
	// compression state, named streams, and other documented replacement data.
	return nil
}

func makeAgentReplacementRemovable(_ *os.File) error {
	return nil
}

func publishAgentFileExclusive(sourcePath, destinationPath string) error {
	source, err := windows.UTF16PtrFromString(sourcePath)
	if err != nil {
		return err
	}
	destination, err := windows.UTF16PtrFromString(destinationPath)
	if err != nil {
		return err
	}
	return windows.MoveFile(source, destination)
}

func syncAgentParentDirectory(_ string) error {
	// MoveFile/ReplaceFileW provide the Windows publication primitive. Windows
	// does not expose a portable directory-handle fsync equivalent through Go.
	return nil
}

func commitAgentReplacement(locked *lockedAgentFile, replacementFile *os.File, replacementPath string, replacementInfo os.FileInfo) (bool, error) {
	if err := locked.verifyUnchanged(); err != nil {
		return true, fmt.Errorf("verify destination before replacement: %w", err)
	}
	if err := verifyAgentReplacementPath(replacementPath, replacementInfo); err != nil {
		return false, err
	}
	if err := replacementFile.Close(); err != nil {
		return true, fmt.Errorf("close replacement before commit: %w", err)
	}
	if err := locked.file.Close(); err != nil {
		locked.file = nil
		return true, fmt.Errorf("close locked source before replacement: %w", err)
	}
	locked.file = nil
	if err := verifyAgentReplacementPath(replacementPath, replacementInfo); err != nil {
		return false, err
	}

	destination, err := windows.UTF16PtrFromString(locked.path)
	if err != nil {
		return true, fmt.Errorf("encode destination path: %w", err)
	}
	replacement, err := windows.UTF16PtrFromString(replacementPath)
	if err != nil {
		return true, fmt.Errorf("encode replacement path: %w", err)
	}
	if replaceErr := replaceAgentFile(destination, replacement); replaceErr != nil {
		cleanupSafe := windowsReplacementFailureCleanupSafe(locked, replacementPath, replacementInfo)
		if !cleanupSafe {
			if pathInfo, statErr := agentFilePathInfo(replacementPath); statErr == nil && os.SameFile(replacementInfo, pathInfo) {
				return false, fmt.Errorf("replace file: %w; complete replacement retained at %q for recovery", replaceErr, replacementPath)
			}
			return false, fmt.Errorf("replace file: %w; destination state is ambiguous", replaceErr)
		}
		return true, fmt.Errorf("replace file: %w", replaceErr)
	}
	publishedInfo, err := agentFilePathInfo(locked.path)
	if err != nil || !publishedInfo.Mode().IsRegular() || !os.SameFile(replacementInfo, publishedInfo) {
		return false, fmt.Errorf("replacement destination changed during ReplaceFileW publication")
	}
	return true, nil
}

func windowsReplacementFailureCleanupSafe(locked *lockedAgentFile, replacementPath string, replacementInfo os.FileInfo) bool {
	pathInfo, err := agentFilePathInfo(replacementPath)
	if err != nil || !os.SameFile(replacementInfo, pathInfo) {
		return false
	}
	destination, err := os.Open(locked.path)
	if err != nil {
		return false
	}
	defer destination.Close()
	destinationInfo, err := destination.Stat()
	if err != nil || !os.SameFile(locked.info, destinationInfo) {
		return false
	}
	content, err := os.ReadFile(locked.path)
	if err != nil || !bytes.Equal(content, locked.content) {
		return false
	}
	metadata, err := snapshotAgentFileMetadata(destination)
	return err == nil && sameAgentFileMetadata(locked.metadata, metadata)
}

// openAgentFileForMutation, lockAgentFile, and unlockAgentFile are the
// decomposed remote locking API. Production mutation uses
// openAndLockAgentFileForMutation (identity mutex + timeout). These names
// remain so callers can open/lock separately. Open still uses share-permissive
// CreateFile with OPEN_REPARSE_POINT so a final-component symlink is not
// followed.
func openAgentFileForMutation(path string) (*os.File, error) {
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		pathPtr,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, fmt.Errorf("wrap Windows file handle")
	}
	return file, nil
}

func lockAgentFile(file *os.File) error {
	handle := windows.Handle(file.Fd())
	var overlapped windows.Overlapped
	return windows.LockFileEx(handle, windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &overlapped)
}

func unlockAgentFile(file *os.File) error {
	handle := windows.Handle(file.Fd())
	var overlapped windows.Overlapped
	return windows.UnlockFileEx(handle, 0, 1, 0, &overlapped)
}
