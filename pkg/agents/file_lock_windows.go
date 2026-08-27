//go:build windows

package agents

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var replaceFileW = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReplaceFileW")

var replaceAgentFile = func(destination, replacement, backup *uint16) error {
	result, _, callErr := replaceFileW.Call(
		uintptr(unsafe.Pointer(destination)),
		uintptr(unsafe.Pointer(replacement)),
		uintptr(unsafe.Pointer(backup)),
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
	attributes               uint32
	volumeSerial             uint32
	linkCount                uint32
	fileIndexHigh            uint32
	fileIndexLow             uint32
	creationTimeHigh         uint32
	creationTimeLow          uint32
	writeTimeHigh            uint32
	writeTimeLow             uint32
	security                 [sha256.Size]byte
	mandatoryLabel           [sha256.Size]byte
	resourceAttributes       [sha256.Size]byte
	centralAccessPolicyScope [sha256.Size]byte
	namedStreamCount         uint32
}

func windowsAgentPathPointer(path string) (*uint16, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve Windows path: %w", err)
	}
	absolute = filepath.Clean(absolute)
	if strings.HasPrefix(absolute, `\\?\`) {
	} else if strings.HasPrefix(absolute, `\\.\`) {
		return nil, fmt.Errorf("refusing Windows device-namespace path %q", path)
	} else if strings.HasPrefix(absolute, `\\`) {
		absolute = `\\?\UNC\` + strings.TrimPrefix(absolute, `\\`)
	} else {
		absolute = `\\?\` + absolute
	}
	if err := validateWindowsExtendedFilePath(absolute); err != nil {
		return nil, fmt.Errorf("refusing Windows extended path %q: %w", path, err)
	}
	return windows.UTF16PtrFromString(absolute)
}

func validateWindowsExtendedFilePath(path string) error {
	const prefix = `\\?\`
	if !strings.HasPrefix(path, prefix) {
		return fmt.Errorf("missing extended-length prefix")
	}
	remainder := path[len(prefix):]
	if len(remainder) >= 3 &&
		((remainder[0] >= 'A' && remainder[0] <= 'Z') || (remainder[0] >= 'a' && remainder[0] <= 'z')) &&
		remainder[1] == ':' && remainder[2] == '\\' {
		return nil
	}

	const uncPrefix = `UNC\`
	if len(remainder) >= len(uncPrefix) && strings.EqualFold(remainder[:len(uncPrefix)], uncPrefix) {
		components := strings.Split(remainder[len(uncPrefix):], `\`)
		if len(components) < 2 || components[0] == "" || components[1] == "" || components[0] == "." || components[0] == "?" || components[0] == ".." {
			return fmt.Errorf("UNC path must contain a non-device server and share")
		}
		if strings.EqualFold(components[1], "pipe") || strings.EqualFold(components[1], "mailslot") || strings.EqualFold(components[1], "IPC$") {
			return fmt.Errorf("UNC IPC namespace %q is not a filesystem share", components[1])
		}
		return nil
	}

	return fmt.Errorf("only drive-letter and UNC filesystem paths are supported")
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
	name, err := windowsAgentPathPointer(path)
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

// openAgentFileForInspection opens the final path object itself. A reparse
// point that replaces a previously inspected regular file must never redirect
// a verification read into another file before the later identity check.
func openAgentFileForInspection(path string) (*os.File, error) {
	name, err := windowsAgentPathPointer(path)
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

// windowsAgentNamedStreamCount enumerates streams through the already-open
// file handle, so the result cannot be redirected through a raced pathname.
// The default stream is either unnamed or reported as "::$DATA" and is not an
// alternate data stream. A bounded buffer is sufficient for the no-ADS case we
// accept; overflow is a fail-closed metadata error rather than an invitation to
// publish a file whose complete stream set was not observed.
func windowsAgentNamedStreamCount(file *os.File) (uint32, error) {
	if file == nil {
		return 0, fmt.Errorf("agent-file handle is nil")
	}
	const (
		streamInfoHeaderSize = 24
		streamInfoBufferSize = 4 << 10
	)
	// FILE_STREAM_INFO requires 8-byte alignment, including on 32-bit Windows
	// where a uint64 need not have 8-byte Go alignment. Overallocate and slice to
	// an explicitly aligned address.
	storage := make([]byte, streamInfoBufferSize+7)
	misalignment := uintptr(unsafe.Pointer(&storage[0])) % 8
	alignmentOffset := 0
	if misalignment != 0 {
		alignmentOffset = int(8 - misalignment)
	}
	buffer := storage[alignmentOffset : alignmentOffset+streamInfoBufferSize]
	err := windows.GetFileInformationByHandleEx(
		windows.Handle(file.Fd()),
		windows.FileStreamInfo,
		&buffer[0],
		uint32(len(buffer)),
	)
	if errors.Is(err, windows.ERROR_HANDLE_EOF) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("enumerate file streams: %w", err)
	}

	var named uint32
	for offset := 0; ; {
		if len(buffer)-offset < streamInfoHeaderSize {
			return 0, fmt.Errorf("malformed file-stream information: truncated header at offset %d", offset)
		}
		nextOffset := binary.LittleEndian.Uint32(buffer[offset : offset+4])
		nameLength := binary.LittleEndian.Uint32(buffer[offset+4 : offset+8])
		entryEnd := len(buffer)
		if nextOffset != 0 {
			remaining := uint32(len(buffer) - offset)
			if nextOffset < streamInfoHeaderSize || nextOffset%8 != 0 || nextOffset > remaining {
				return 0, fmt.Errorf("malformed file-stream information: invalid next offset %d at offset %d", nextOffset, offset)
			}
			entryEnd = offset + int(nextOffset)
		}
		nameStart := offset + streamInfoHeaderSize
		if nameLength%2 != 0 || nameLength > uint32(entryEnd-nameStart) {
			return 0, fmt.Errorf("malformed file-stream information: invalid name length %d at offset %d", nameLength, offset)
		}
		nameUnits := make([]uint16, int(nameLength/2))
		for i := range nameUnits {
			nameUnits[i] = binary.LittleEndian.Uint16(buffer[nameStart+2*i : nameStart+2*i+2])
		}
		name := windows.UTF16ToString(nameUnits)
		if name != "" && !strings.EqualFold(name, "::$DATA") {
			named++
		}
		if nextOffset == 0 {
			return named, nil
		}
		offset += int(nextOffset)
	}
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
	_, resourceAttributesDigest, err := windowsSecurityComponent(file, windows.ATTRIBUTE_SECURITY_INFORMATION)
	if err != nil {
		return agentFileMetadataSnapshot{}, fmt.Errorf("read resource attributes: %w", err)
	}
	_, scopeDigest, err := windowsSecurityComponent(file, windows.SCOPE_SECURITY_INFORMATION)
	if err != nil {
		return agentFileMetadataSnapshot{}, fmt.Errorf("read central access policy scope: %w", err)
	}
	namedStreamCount, err := windowsAgentNamedStreamCount(file)
	if err != nil {
		return agentFileMetadataSnapshot{}, fmt.Errorf("read alternate data streams: %w", err)
	}
	return agentFileMetadataSnapshot{
		attributes:               info.FileAttributes,
		volumeSerial:             info.VolumeSerialNumber,
		linkCount:                info.NumberOfLinks,
		fileIndexHigh:            info.FileIndexHigh,
		fileIndexLow:             info.FileIndexLow,
		creationTimeHigh:         info.CreationTime.HighDateTime,
		creationTimeLow:          info.CreationTime.LowDateTime,
		writeTimeHigh:            info.LastWriteTime.HighDateTime,
		writeTimeLow:             info.LastWriteTime.LowDateTime,
		security:                 securityDigest,
		mandatoryLabel:           labelDigest,
		resourceAttributes:       resourceAttributesDigest,
		centralAccessPolicyScope: scopeDigest,
		namedStreamCount:         namedStreamCount,
	}, nil
}

func sameAgentFileMetadata(a, b agentFileMetadataSnapshot) bool {
	return a == b
}

func sameWindowsAgentAccessPolicy(a, b agentFileMetadataSnapshot) bool {
	return a.security == b.security &&
		a.mandatoryLabel == b.mandatoryLabel &&
		a.resourceAttributes == b.resourceAttributes &&
		a.centralAccessPolicyScope == b.centralAccessPolicyScope
}

func verifyAgentFileHasSingleLink(file *os.File) error {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &info); err != nil {
		return fmt.Errorf("inspect published file links: %w", err)
	}
	if info.NumberOfLinks != 1 {
		return fmt.Errorf("published file has %d hard links, want 1", info.NumberOfLinks)
	}
	namedStreamCount, err := windowsAgentNamedStreamCount(file)
	if err != nil {
		return fmt.Errorf("inspect published file alternate data streams: %w", err)
	}
	if namedStreamCount != 0 {
		return fmt.Errorf("published file has %d alternate data streams, want 0", namedStreamCount)
	}
	return nil
}

// closeWindowsAgentPrivateFileAfterFailure closes a candidate that cannot be
// returned safely and reports whether its never-deleted private pathname still
// identifies the inode we created. Failed candidates are retained deliberately;
// deleting by pathname after an identity check would race a peer name change.
func closeWindowsAgentPrivateFileAfterFailure(file *os.File, path string, expected os.FileInfo, operationErr error) error {
	if file == nil {
		return operationErr
	}
	closeErr := file.Close()
	pathInfo, pathErr := agentFilePathInfo(path)

	var retentionErr error
	switch {
	case expected != nil && pathErr == nil && pathInfo.Mode().IsRegular() && os.SameFile(expected, pathInfo):
		retentionErr = fmt.Errorf("private replacement retained at %q for recovery", path)
	case expected == nil:
		retentionErr = fmt.Errorf("private replacement was allocated at %q but its retained identity could not be verified", path)
	case pathErr != nil:
		retentionErr = fmt.Errorf("private replacement was allocated at %q but its retained pathname could not be verified: %w", path, pathErr)
	default:
		retentionErr = fmt.Errorf("private replacement was allocated at %q but that pathname no longer identifies the created file", path)
	}
	return errors.Join(operationErr, closeErr, retentionErr)
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

func windowsSecurityComponent(file *os.File, securityInformation windows.SECURITY_INFORMATION) (*windows.SECURITY_DESCRIPTOR, [sha256.Size]byte, error) {
	security, err := windows.GetSecurityInfo(
		windows.Handle(file.Fd()),
		windows.SE_FILE_OBJECT,
		securityInformation,
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

func windowsMandatoryLabel(file *os.File) (*windows.SECURITY_DESCRIPTOR, [sha256.Size]byte, error) {
	return windowsSecurityComponent(file, windows.LABEL_SECURITY_INFORMATION)
}

func windowsAgentReplacementCreationAttributes(sourceAttributes uint32) uint32 {
	if sourceAttributes&windows.FILE_ATTRIBUTE_ENCRYPTED != 0 {
		return windows.FILE_ATTRIBUTE_ENCRYPTED
	}
	return windows.FILE_ATTRIBUTE_NORMAL
}

func createAgentReplacementFile(locked *lockedAgentFile) (*os.File, string, os.FileInfo, error) {
	if locked.metadata.namedStreamCount != 0 {
		return nil, "", nil, fmt.Errorf("refusing to replace agent file with %d alternate data streams", locked.metadata.namedStreamCount)
	}
	dir := filepath.Dir(locked.path)
	for attempt := 0; attempt < 64; attempt++ {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return nil, "", nil, fmt.Errorf("generate replacement name: %w", err)
		}
		path := filepath.Join(dir, ".bv-replace-"+hex.EncodeToString(random[:]))
		name, err := windowsAgentPathPointer(path)
		if err != nil {
			return nil, "", nil, err
		}
		access := uint32(windows.GENERIC_READ | windows.GENERIC_WRITE | windows.WRITE_DAC | windows.WRITE_OWNER)
		creationAttributes := windowsAgentReplacementCreationAttributes(locked.metadata.attributes)
		handle, err := windows.CreateFile(
			name,
			access|windows.ACCESS_SYSTEM_SECURITY,
			// Deny every peer open until the source's complete access policy has
			// been applied. Otherwise a principal permitted by the parent but denied
			// by the source could read rewritten bytes from the temp name.
			0,
			nil,
			windows.CREATE_NEW,
			// EFS must protect an encrypted source's candidate at creation, before
			// the first byte is written. The candidate is promoted to the long-lived
			// destination, so never give it FILE_ATTRIBUTE_TEMPORARY semantics.
			creationAttributes,
			0,
		)
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_PRIVILEGE_NOT_HELD) {
			// SCOPE_SECURITY_INFORMATION can be set only through a handle granted
			// ACCESS_SYSTEM_SECURITY. Ordinary callers normally lack the required
			// privilege, so fall back to the ordinary handle and proceed only when
			// the inherited candidate scope already equals the source scope.
			handle, err = windows.CreateFile(
				name,
				access,
				0,
				nil,
				windows.CREATE_NEW,
				creationAttributes,
				0,
			)
		}
		if errors.Is(err, windows.ERROR_FILE_EXISTS) || errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			continue
		}
		if err != nil {
			return nil, "", nil, err
		}
		file := os.NewFile(uintptr(handle), path)
		openedInfo, err := file.Stat()
		if err != nil {
			// No trusted handle identity means the path cannot be cleaned safely.
			return nil, "", nil, closeWindowsAgentPrivateFileAfterFailure(file, path, nil, fmt.Errorf("stat secure replacement: %w", err))
		}
		fail := func(operationErr error) (*os.File, string, os.FileInfo, error) {
			return nil, "", nil, closeWindowsAgentPrivateFileAfterFailure(file, path, openedInfo, operationErr)
		}
		pathInfo, err := agentFilePathInfo(path)
		if err != nil {
			return fail(fmt.Errorf("stat secure replacement path: %w", err))
		}
		if !pathInfo.Mode().IsRegular() || !os.SameFile(openedInfo, pathInfo) {
			return fail(fmt.Errorf("replacement path changed during creation"))
		}
		// Bind the candidate to the still-locked source before any content is
		// written. Denying share access protects the open handle, but it does not
		// make the named file private after an early write or sync failure closes
		// that handle. Copying the source security policy while the candidate is
		// empty ensures every byte-bearing recovery artifact is already no more
		// accessible than the source.
		if err := locked.verifyUnchanged(); err != nil {
			return fail(fmt.Errorf("verify destination before securing replacement: %w", err))
		}
		if err := prepareAgentReplacementMetadata(locked.file, file, locked.info.Mode()); err != nil {
			return fail(fmt.Errorf("secure empty replacement: %w", err))
		}
		if err := locked.verifyUnchanged(); err != nil {
			return fail(fmt.Errorf("verify destination after securing replacement: %w", err))
		}
		securedInfo, err := file.Stat()
		if err != nil {
			return fail(fmt.Errorf("stat secured replacement: %w", err))
		}
		pathInfo, err = agentFilePathInfo(path)
		if err != nil {
			return fail(fmt.Errorf("restat secured replacement path: %w", err))
		}
		if securedInfo.Size() != 0 || !securedInfo.Mode().IsRegular() || !pathInfo.Mode().IsRegular() ||
			!os.SameFile(openedInfo, securedInfo) || !os.SameFile(securedInfo, pathInfo) {
			return fail(fmt.Errorf("secured replacement changed during creation"))
		}
		return file, path, securedInfo, nil
	}
	return nil, "", nil, fmt.Errorf("could not allocate a unique replacement name")
}

func allocateAgentReplacementBackupPath(dir string) (string, *uint16, error) {
	for attempt := 0; attempt < 64; attempt++ {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", nil, fmt.Errorf("generate replacement backup name: %w", err)
		}
		path := filepath.Join(dir, ".bv-backup-"+hex.EncodeToString(random[:]))
		if _, err := agentFilePathInfo(path); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", nil, fmt.Errorf("inspect replacement backup path: %w", err)
		}
		name, err := windowsAgentPathPointer(path)
		if err != nil {
			return "", nil, err
		}
		return path, name, nil
	}
	return "", nil, fmt.Errorf("could not allocate a unique replacement backup name")
}

func windowsAgentPathMutexName(file *os.File) (*uint16, error) {
	if file == nil {
		return nil, fmt.Errorf("agent-file handle is nil")
	}
	const (
		volumeNameNT                 = 0x2
		initialFinalPathBufferLength = 256
		maximumFinalPathBufferLength = 32_768
	)
	bufferLength := uint32(initialFinalPathBufferLength)
	var stablePath string
	for {
		buffer := make([]uint16, bufferLength)
		length, err := windows.GetFinalPathNameByHandle(
			windows.Handle(file.Fd()),
			&buffer[0],
			uint32(len(buffer)),
			volumeNameNT,
		)
		runtime.KeepAlive(file)
		if err != nil {
			return nil, fmt.Errorf("resolve normalized final agent-file path: %w", err)
		}
		if length == 0 {
			return nil, fmt.Errorf("resolve normalized final agent-file path: empty result")
		}
		if length < uint32(len(buffer)) {
			stablePath = windows.UTF16ToString(buffer[:length])
			break
		}
		// A too-small buffer returns the required size including its terminator.
		if length > maximumFinalPathBufferLength {
			return nil, fmt.Errorf("normalized final agent-file path requires %d UTF-16 code units", length)
		}
		bufferLength = length
	}
	if stablePath == "" {
		return nil, fmt.Errorf("resolve normalized final agent-file path: empty path")
	}
	// FILE_NAME_NORMALIZED plus VOLUME_NAME_NT resolves drive-letter, mount-point,
	// junction, and short-name spellings to the same device path. Windows paths
	// are ordinarily case-insensitive; folding deliberately over-serializes the
	// uncommon case-distinct entries of a case-sensitive directory. The separate
	// file-ID mutex still coordinates distinct hard-link names of the current
	// inode.
	stablePath = strings.ToUpper(stablePath)
	digest := sha256.Sum256([]byte(stablePath))
	return windows.UTF16PtrFromString(`Global\bv-agent-path-` + hex.EncodeToString(digest[:]))
}

func windowsAgentIdentityMutexName(identity windows.ByHandleFileInformation) (*uint16, error) {
	return windows.UTF16PtrFromString(fmt.Sprintf(
		`Global\bv-agent-file-%08x-%08x%08x`,
		identity.VolumeSerialNumber,
		identity.FileIndexHigh,
		identity.FileIndexLow,
	))
}

func acquireWindowsAgentMutex(name *uint16, timeout time.Duration) (windows.Handle, error) {
	if timeout <= 0 {
		return 0, errAgentFileBusy
	}
	mutex, err := windows.CreateMutex(nil, false, name)
	if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return 0, err
	}

	waitMillisDuration := timeout / time.Millisecond
	if timeout%time.Millisecond != 0 {
		waitMillisDuration++
	}
	var waitMillis uint32
	if waitMillisDuration >= time.Duration(windows.INFINITE) {
		// INFINITE has sentinel semantics. Cap very large caller-provided
		// durations at the largest finite Win32 timeout instead.
		waitMillis = uint32(windows.INFINITE - 1)
	} else {
		waitMillis = uint32(waitMillisDuration)
	}
	event, waitErr := windows.WaitForSingleObject(mutex, waitMillis)
	if waitErr != nil {
		return 0, errors.Join(waitErr, windows.CloseHandle(mutex))
	}
	if event == uint32(windows.WAIT_TIMEOUT) {
		return 0, errors.Join(errAgentFileBusy, windows.CloseHandle(mutex))
	}
	if event != windows.WAIT_OBJECT_0 && event != windows.WAIT_ABANDONED {
		return 0, errors.Join(
			fmt.Errorf("wait for agent-file mutex returned %#x", event),
			windows.CloseHandle(mutex),
		)
	}
	return mutex, nil
}

func releaseWindowsAgentMutex(mutex windows.Handle) error {
	if mutex == 0 {
		return nil
	}
	return errors.Join(windows.ReleaseMutex(mutex), windows.CloseHandle(mutex))
}

func openAndLockAgentFileForMutation(path string, timeout time.Duration) (*os.File, func() error, error) {
	pathName, err := windowsAgentPathPointer(path)
	if err != nil {
		return nil, nil, err
	}
	handle, err := windows.CreateFile(
		pathName,
		windows.GENERIC_READ,
		// ReplaceFileW opens the replaced file with GENERIC_READ, DELETE, and
		// SYNCHRONIZE, so read/delete sharing is sufficient. Denying write sharing
		// prevents a new path writer from changing source bytes after verification.
		windows.FILE_SHARE_READ|windows.FILE_SHARE_DELETE,
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
	pathMutexName, err := windowsAgentPathMutexName(file)
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	identityMutexName, err := windowsAgentIdentityMutexName(identity)
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}

	// Win32 mutex ownership belongs to an OS thread, not a Go goroutine. Keep
	// this goroutine pinned from both acquisitions through both releases so
	// migration cannot leak a lock or accidentally exploit recursive ownership.
	// The stable pathname mutex spans ReplaceFileW's inode-identity rollover; the
	// file-ID mutex continues to serialize aliases of the current inode.
	runtime.LockOSThread()
	deadline := time.Now().Add(timeout)
	pathMutex, err := acquireWindowsAgentMutex(pathMutexName, time.Until(deadline))
	if err != nil {
		runtime.UnlockOSThread()
		return nil, nil, errors.Join(err, file.Close())
	}
	identityMutex, err := acquireWindowsAgentMutex(identityMutexName, time.Until(deadline))
	if err != nil {
		releaseErr := releaseWindowsAgentMutex(pathMutex)
		runtime.UnlockOSThread()
		return nil, nil, errors.Join(err, releaseErr, file.Close())
	}

	unlock := func() error {
		identityErr := releaseWindowsAgentMutex(identityMutex)
		pathErr := releaseWindowsAgentMutex(pathMutex)
		runtime.UnlockOSThread()
		return errors.Join(identityErr, pathErr)
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
	var replacementInfo windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(replacement.Fd()), &replacementInfo); err != nil {
		return fmt.Errorf("stat replacement metadata: %w", err)
	}
	if replacementInfo.NumberOfLinks != 1 {
		return fmt.Errorf("refusing to publish replacement candidate with %d hard links", replacementInfo.NumberOfLinks)
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
	if err := copyWindowsAgentSecurityComponent(
		source,
		replacement,
		windows.ATTRIBUTE_SECURITY_INFORMATION,
		"resource attributes",
	); err != nil {
		return err
	}
	if err := copyWindowsAgentSecurityComponent(
		source,
		replacement,
		windows.SCOPE_SECURITY_INFORMATION,
		"central access policy scope",
	); err != nil {
		return err
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
	if err := verifyWindowsAgentReplacementMetadata(source, replacement); err != nil {
		return err
	}
	// ReplaceFileW additionally merges destination attributes, compression state,
	// and other documented replacement data. Named streams are rejected above:
	// its asymmetric stream merge cannot be proven exact if a peer adds a
	// colliding candidate stream after this no-share handle is closed. Encryption
	// and all access-policy components are already exact before this candidate is
	// written.
	return nil
}

func copyWindowsAgentSecurityComponent(source, replacement *os.File, securityInformation windows.SECURITY_INFORMATION, description string) error {
	return copyWindowsAgentSecurityComponentUsing(
		source,
		replacement,
		securityInformation,
		description,
		windowsSecurityComponent,
		windows.SetSecurityInfo,
	)
}

func copyWindowsAgentSecurityComponentUsing(
	source, replacement *os.File,
	securityInformation windows.SECURITY_INFORMATION,
	description string,
	query func(*os.File, windows.SECURITY_INFORMATION) (*windows.SECURITY_DESCRIPTOR, [sha256.Size]byte, error),
	set func(windows.Handle, windows.SE_OBJECT_TYPE, windows.SECURITY_INFORMATION, *windows.SID, *windows.SID, *windows.ACL, *windows.ACL) error,
) error {
	sourceSecurity, sourceDigest, err := query(source, securityInformation)
	if err != nil {
		return fmt.Errorf("read source %s: %w", description, err)
	}
	_, replacementDigest, err := query(replacement, securityInformation)
	if err != nil {
		return fmt.Errorf("read replacement %s: %w", description, err)
	}
	if sourceDigest == replacementDigest {
		return nil
	}

	sacl, _, saclErr := sourceSecurity.SACL()
	if saclErr != nil && !errors.Is(saclErr, windows.ERROR_OBJECT_NOT_FOUND) {
		return fmt.Errorf("read source %s ACL: %w", description, saclErr)
	}
	if err := set(
		windows.Handle(replacement.Fd()),
		windows.SE_FILE_OBJECT,
		securityInformation,
		nil,
		nil,
		nil,
		sacl,
	); err != nil {
		runtime.KeepAlive(sourceSecurity)
		return fmt.Errorf("copy source %s: %w", description, err)
	}
	runtime.KeepAlive(sourceSecurity)
	_, replacementDigest, err = query(replacement, securityInformation)
	if err != nil {
		return fmt.Errorf("verify replacement %s: %w", description, err)
	}
	if sourceDigest != replacementDigest {
		return fmt.Errorf("replacement %s differs from source", description)
	}
	return nil
}

func verifyWindowsAgentReplacementMetadata(source, replacement *os.File) error {
	sourceMetadata, err := snapshotAgentFileMetadata(source)
	if err != nil {
		return fmt.Errorf("restat source metadata: %w", err)
	}
	replacementMetadata, err := snapshotAgentFileMetadata(replacement)
	if err != nil {
		return fmt.Errorf("restat replacement metadata: %w", err)
	}
	return verifyWindowsAgentReplacementMetadataSnapshots(sourceMetadata, replacementMetadata)
}

func verifyWindowsAgentReplacementMetadataSnapshots(sourceMetadata, replacementMetadata agentFileMetadataSnapshot) error {
	if sourceMetadata.linkCount != 1 {
		return fmt.Errorf("refusing to replace agent file with %d hard links", sourceMetadata.linkCount)
	}
	if replacementMetadata.linkCount != 1 {
		return fmt.Errorf("refusing to publish replacement candidate with %d hard links", replacementMetadata.linkCount)
	}
	if sourceMetadata.namedStreamCount != 0 {
		return fmt.Errorf("refusing to replace agent file with %d alternate data streams", sourceMetadata.namedStreamCount)
	}
	if replacementMetadata.namedStreamCount != 0 {
		return fmt.Errorf("refusing to publish replacement candidate with %d alternate data streams", replacementMetadata.namedStreamCount)
	}
	if replacementMetadata.security != sourceMetadata.security {
		return fmt.Errorf("replacement owner/group/DACL differs from source")
	}
	if replacementMetadata.mandatoryLabel != sourceMetadata.mandatoryLabel {
		return fmt.Errorf("replacement mandatory integrity label differs from source")
	}
	if replacementMetadata.resourceAttributes != sourceMetadata.resourceAttributes {
		return fmt.Errorf("replacement resource attributes differ from source")
	}
	if replacementMetadata.centralAccessPolicyScope != sourceMetadata.centralAccessPolicyScope {
		return fmt.Errorf("replacement central access policy scope differs from source")
	}
	if (replacementMetadata.attributes^sourceMetadata.attributes)&windows.FILE_ATTRIBUTE_ENCRYPTED != 0 {
		return fmt.Errorf("replacement EFS encryption state differs from source")
	}
	return nil
}

func makeAgentReplacementRemovable(_ *os.File) error {
	return nil
}

func makeAgentReplacementPrivateAfterFailure(_ *os.File) error {
	// Creation establishes EFS state and copies the source DACL, mandatory label,
	// resource attributes, and central-policy scope while the candidate is still
	// empty. Every byte-bearing unpublished file is therefore already no more
	// accessible than the source, and Windows has no Unix-style mode to add here.
	return nil
}

func finalizeAgentReplacementAccess(_, _ *os.File, _ os.FileMode) error {
	// Windows access is finalized by prepareAgentReplacementMetadata while the
	// no-share candidate handle is still open.
	return nil
}

func publishAgentFileExclusive(sourcePath, destinationPath string) (bool, error) {
	source, err := windowsAgentPathPointer(sourcePath)
	if err != nil {
		return false, err
	}
	destination, err := windowsAgentPathPointer(destinationPath)
	if err != nil {
		return false, err
	}
	if err := windows.MoveFile(source, destination); err != nil {
		return false, err
	}
	return true, nil
}

func syncAgentParentDirectory(_ string) error {
	// MoveFile/ReplaceFileW provide the Windows publication primitive. Windows
	// does not expose a portable directory-handle fsync equivalent through Go.
	return nil
}

func removeCommittedAgentBackup(locked *lockedAgentFile, backupPath string) error {
	pathInfo, err := agentFilePathInfo(backupPath)
	if err != nil {
		return fmt.Errorf("inspect committed backup: %w", err)
	}
	if !pathInfo.Mode().IsRegular() || !os.SameFile(locked.info, pathInfo) {
		return fmt.Errorf("committed backup path does not identify the replaced source")
	}

	name, err := windowsAgentPathPointer(backupPath)
	if err != nil {
		return err
	}
	handle, err := windows.CreateFile(
		name,
		windows.GENERIC_READ|windows.DELETE|windows.SYNCHRONIZE,
		// Once this handle is open, deny new delete/rename opens so cleanup stays
		// bound to the backup link whose identity was just verified, and deny new
		// writers while its original bytes and metadata are revalidated.
		windows.FILE_SHARE_READ,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		return fmt.Errorf("open committed backup for exact cleanup: %w", err)
	}
	backupFile := os.NewFile(uintptr(handle), backupPath)
	backupInfo, statErr := backupFile.Stat()
	if statErr != nil || !backupInfo.Mode().IsRegular() || !os.SameFile(locked.info, backupInfo) {
		_ = backupFile.Close()
		if statErr != nil {
			return fmt.Errorf("stat committed backup handle: %w", statErr)
		}
		return fmt.Errorf("committed backup changed before exact cleanup")
	}
	var handleInfo windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &handleInfo); err != nil {
		_ = backupFile.Close()
		return fmt.Errorf("inspect committed backup links: %w", err)
	}
	if handleInfo.NumberOfLinks != 1 {
		_ = backupFile.Close()
		return fmt.Errorf("refusing to remove committed backup with %d hard links", handleInfo.NumberOfLinks)
	}
	backupMetadata, err := snapshotAgentFileMetadata(backupFile)
	if err != nil {
		_ = backupFile.Close()
		return fmt.Errorf("snapshot committed backup metadata: %w", err)
	}
	if !sameAgentFileSnapshot(locked.info, backupInfo) || !sameAgentFileMetadata(locked.metadata, backupMetadata) {
		_ = backupFile.Close()
		return fmt.Errorf("refusing to remove committed backup whose original metadata changed")
	}
	backupContent, err := readAgentFileExactly(backupFile, int64(len(locked.content)))
	if err != nil {
		_ = backupFile.Close()
		return fmt.Errorf("read committed backup before cleanup: %w", err)
	}
	verifiedInfo, err := backupFile.Stat()
	if err != nil {
		_ = backupFile.Close()
		return fmt.Errorf("restat committed backup before cleanup: %w", err)
	}
	verifiedMetadata, err := snapshotAgentFileMetadata(backupFile)
	if err != nil {
		_ = backupFile.Close()
		return fmt.Errorf("resnapshot committed backup before cleanup: %w", err)
	}
	if !bytes.Equal(backupContent, locked.content) ||
		!sameAgentFileSnapshot(backupInfo, verifiedInfo) ||
		!sameAgentFileMetadata(backupMetadata, verifiedMetadata) {
		_ = backupFile.Close()
		return fmt.Errorf("refusing to remove committed backup whose original bytes or metadata changed")
	}

	dispositionFlags := uint32(
		windows.FILE_DISPOSITION_DELETE |
			windows.FILE_DISPOSITION_POSIX_SEMANTICS |
			windows.FILE_DISPOSITION_IGNORE_READONLY_ATTRIBUTE,
	)
	if err := windows.SetFileInformationByHandle(
		handle,
		windows.FileDispositionInfoEx,
		(*byte)(unsafe.Pointer(&dispositionFlags)),
		uint32(unsafe.Sizeof(dispositionFlags)),
	); err != nil {
		_ = backupFile.Close()
		return fmt.Errorf("mark committed backup for exact cleanup: %w", err)
	}
	backupCloseErr := backupFile.Close()
	sourceCloseErr := locked.file.Close()
	locked.file = nil
	if closeErr := errors.Join(backupCloseErr, sourceCloseErr); closeErr != nil {
		return fmt.Errorf("close committed backup handles: %w", closeErr)
	}
	if _, err := agentFilePathInfo(backupPath); err == nil {
		return fmt.Errorf("committed backup name survived handle cleanup")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("verify committed backup cleanup: %w", err)
	}
	return nil
}

// openVerifiedPublishedAgentReplacement opens the published replacement while
// denying new write and delete opens, verifies its exact prepared identity and
// bytes, and returns the still-open handle. The caller must keep that handle
// open until the displaced source backup has either been removed or retained.
func openVerifiedPublishedAgentReplacement(path string, expectedInfo os.FileInfo, expectedMetadata agentFileMetadataSnapshot, expectedContent []byte) (*os.File, error) {
	if expectedInfo == nil {
		return nil, fmt.Errorf("prepared replacement identity is unavailable")
	}
	if expectedInfo.Size() != int64(len(expectedContent)) {
		return nil, fmt.Errorf("prepared replacement size %d differs from captured content size %d", expectedInfo.Size(), len(expectedContent))
	}
	name, err := windowsAgentPathPointer(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		name,
		windows.GENERIC_READ,
		// Readers may coexist, but no peer may acquire write or delete access
		// between this verification and cleanup of the original backup.
		windows.FILE_SHARE_READ,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("open published replacement without write/delete sharing: %w", err)
	}
	file := os.NewFile(uintptr(handle), path)
	fail := func(verifyErr error) (*os.File, error) {
		return nil, errors.Join(verifyErr, file.Close())
	}

	beforeInfo, err := file.Stat()
	if err != nil {
		return fail(fmt.Errorf("stat published replacement handle: %w", err))
	}
	// ReplaceFileW preserves the replaced source's file attributes. In particular,
	// a read-only source legitimately changes the prepared candidate's os.FileMode
	// during publication, so bind identity, size, and replacement write time here
	// and validate the complete source attribute word immediately below.
	if !beforeInfo.Mode().IsRegular() ||
		!os.SameFile(expectedInfo, beforeInfo) ||
		expectedInfo.Size() != beforeInfo.Size() ||
		!expectedInfo.ModTime().Equal(beforeInfo.ModTime()) {
		return fail(fmt.Errorf("published path does not identify the prepared replacement"))
	}
	beforeMetadata, err := snapshotAgentFileMetadata(file)
	if err != nil {
		return fail(fmt.Errorf("snapshot published replacement metadata: %w", err))
	}
	if beforeMetadata.linkCount != 1 {
		return fail(fmt.Errorf("published replacement has %d hard links, want 1", beforeMetadata.linkCount))
	}
	if expectedMetadata.namedStreamCount != 0 || beforeMetadata.namedStreamCount != expectedMetadata.namedStreamCount {
		return fail(fmt.Errorf(
			"published replacement alternate data streams differ from locked source: got %d, want %d",
			beforeMetadata.namedStreamCount,
			expectedMetadata.namedStreamCount,
		))
	}
	if beforeMetadata.attributes != expectedMetadata.attributes {
		return fail(fmt.Errorf(
			"published replacement attributes %#x differ from locked source %#x",
			beforeMetadata.attributes,
			expectedMetadata.attributes,
		))
	}
	if beforeMetadata.creationTimeHigh != expectedMetadata.creationTimeHigh ||
		beforeMetadata.creationTimeLow != expectedMetadata.creationTimeLow {
		return fail(fmt.Errorf("published replacement creation time differs from locked source"))
	}
	if !sameWindowsAgentAccessPolicy(beforeMetadata, expectedMetadata) {
		return fail(fmt.Errorf("published replacement security metadata differs from locked source"))
	}
	pathInfo, err := agentFilePathInfo(path)
	if err != nil || !sameAgentFileSnapshot(beforeInfo, pathInfo) {
		if err != nil {
			return fail(fmt.Errorf("inspect published replacement path: %w", err))
		}
		return fail(fmt.Errorf("published replacement path changed before byte verification"))
	}

	content, err := readAgentFileExactly(file, int64(len(expectedContent)))
	if err != nil {
		return fail(fmt.Errorf("read published replacement bytes: %w", err))
	}
	afterInfo, err := file.Stat()
	if err != nil {
		return fail(fmt.Errorf("restat published replacement handle: %w", err))
	}
	afterMetadata, err := snapshotAgentFileMetadata(file)
	if err != nil {
		return fail(fmt.Errorf("resnapshot published replacement metadata: %w", err))
	}
	pathInfo, err = agentFilePathInfo(path)
	if err != nil {
		return fail(fmt.Errorf("reinspect published replacement path: %w", err))
	}
	if !bytes.Equal(content, expectedContent) ||
		!sameAgentFileSnapshot(beforeInfo, afterInfo) ||
		!sameAgentFileMetadata(beforeMetadata, afterMetadata) ||
		!sameAgentFileSnapshot(afterInfo, pathInfo) {
		return fail(fmt.Errorf("published replacement changed during protected byte verification"))
	}
	return file, nil
}

func commitAgentReplacement(locked *lockedAgentFile, replacementFile *os.File, replacementPath string, replacementInfo os.FileInfo, expectedContent []byte) (bool, error) {
	if err := locked.verifyUnchanged(); err != nil {
		return false, fmt.Errorf("verify destination before replacement: %w", err)
	}
	if err := verifyAgentReplacementPath(replacementPath, replacementInfo); err != nil {
		return false, err
	}
	if err := verifyWindowsAgentReplacementMetadata(locked.file, replacementFile); err != nil {
		return false, err
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
	if err := locked.verifyUnchanged(); err != nil {
		return false, fmt.Errorf("verify destination immediately before replacement: %w", err)
	}
	if err := verifyWindowsAgentReplacementMetadata(locked.file, replacementFile); err != nil {
		return false, fmt.Errorf("verify replacement metadata immediately before replacement: %w", err)
	}
	if err := replacementFile.Close(); err != nil {
		return false, fmt.Errorf("close replacement before commit: %w", err)
	}
	if err := verifyAgentReplacementPath(replacementPath, replacementInfo); err != nil {
		return false, err
	}

	destination, err := windowsAgentPathPointer(locked.path)
	if err != nil {
		return false, fmt.Errorf("encode destination path: %w", err)
	}
	replacement, err := windowsAgentPathPointer(replacementPath)
	if err != nil {
		return false, fmt.Errorf("encode replacement path: %w", err)
	}
	backupPath, backup, err := allocateAgentReplacementBackupPath(filepath.Dir(locked.path))
	if err != nil {
		return false, err
	}
	if replaceErr := replaceAgentFile(destination, replacement, backup); replaceErr != nil {
		if backupInfo, statErr := agentFilePathInfo(backupPath); statErr == nil && os.SameFile(locked.info, backupInfo) {
			// ERROR_UNABLE_TO_MOVE_REPLACEMENT_2 is explicitly documented to move
			// the original to this backup name. Preserve both it and the complete
			// candidate for manual recovery; neither path is deleted on failure.
			return false, fmt.Errorf("replace file: %w; original retained at %q", replaceErr, backupPath)
		}
		if unchangedErr := locked.verifyUnchanged(); unchangedErr == nil {
			return false, fmt.Errorf("replace file: %w; original destination preserved", replaceErr)
		}
		return false, fmt.Errorf("replace file: %w; destination state is ambiguous", replaceErr)
	}
	publishedFile, err := openVerifiedPublishedAgentReplacement(locked.path, replacementInfo, locked.metadata, expectedContent)
	if err != nil {
		if backupInfo, backupErr := agentFilePathInfo(backupPath); backupErr == nil && os.SameFile(locked.info, backupInfo) {
			return true, fmt.Errorf("replacement publication not verified; original retained at %q: %w", backupPath, err)
		}
		// ReplaceFileW reported success, so publication occurred even when the
		// postcondition or backup identity cannot be established. Preserve that
		// state classification so generic cleanup never treats this as an
		// unpublished candidate.
		return true, fmt.Errorf("replacement publication not verified and backup state is ambiguous: %w", err)
	}
	cleanupErr := removeCommittedAgentBackup(locked, backupPath)
	closeErr := publishedFile.Close()
	if cleanupErr != nil {
		return true, fmt.Errorf("replacement verified but original-backup cleanup or retention is ambiguous at %q: %w", backupPath, errors.Join(cleanupErr, closeErr))
	}
	if closeErr != nil {
		return true, fmt.Errorf("replacement verified and backup removed, but closing protected verification handle failed: %w", closeErr)
	}
	return true, nil
}
