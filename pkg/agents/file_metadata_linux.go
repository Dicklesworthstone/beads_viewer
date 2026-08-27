//go:build linux && !android

package agents

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	linuxFSImmutableFlag = 0x00000010
	linuxFSAppendFlag    = 0x00000020
	linuxFSVerityFlag    = 0x00100000

	linuxFSXFlagRealtime          = 0x00000001
	linuxFSXFlagPrealloc          = 0x00000002
	linuxFSXFlagImmutable         = 0x00000008
	linuxFSXFlagAppend            = 0x00000010
	linuxFSXFlagSync              = 0x00000020
	linuxFSXFlagNoAtime           = 0x00000040
	linuxFSXFlagNoDump            = 0x00000080
	linuxFSXFlagRTInherit         = 0x00000100
	linuxFSXFlagProjectInherit    = 0x00000200
	linuxFSXFlagNoSymlinks        = 0x00000400
	linuxFSXFlagExtentSize        = 0x00000800
	linuxFSXFlagExtentSizeInherit = 0x00001000
	linuxFSXFlagNoDefrag          = 0x00002000
	linuxFSXFlagFileStream        = 0x00004000
	linuxFSXFlagDAX               = 0x00008000
	linuxFSXFlagCoWExtentSize     = 0x00010000
	linuxFSXFlagVerity            = 0x00020000
	linuxFSXFlagCasefold          = 0x00040000
	linuxFSXFlagCaseNonPreserving = 0x00080000
	linuxFSXFlagHasAttr           = 0x80000000

	linuxAgentFSXReadOnlyFlags = linuxFSXFlagPrealloc |
		linuxFSXFlagHasAttr |
		linuxFSXFlagVerity |
		linuxFSXFlagCasefold |
		linuxFSXFlagCaseNonPreserving
	linuxAgentFSXDirectoryOnlyFlags = linuxFSXFlagRTInherit |
		linuxFSXFlagNoSymlinks |
		linuxFSXFlagExtentSizeInherit
	linuxAgentFSXSettableFlags = linuxFSXFlagRealtime |
		linuxFSXFlagImmutable |
		linuxFSXFlagAppend |
		linuxFSXFlagSync |
		linuxFSXFlagNoAtime |
		linuxFSXFlagNoDump |
		linuxFSXFlagProjectInherit |
		linuxFSXFlagExtentSize |
		linuxFSXFlagNoDefrag |
		linuxFSXFlagFileStream |
		linuxFSXFlagDAX |
		linuxFSXFlagCoWExtentSize
	linuxAgentFSXKnownFlags = linuxAgentFSXReadOnlyFlags |
		linuxAgentFSXDirectoryOnlyFlags |
		linuxAgentFSXSettableFlags
	// User-controlled regular-file flags that can survive an inode-replacing
	// save. Read-only filesystem state such as EXTENTS is deliberately retained
	// from the freshly created replacement; verity is rejected above because it
	// authenticates the source inode and cannot be transferred.
	linuxAgentCopyableFlags = 0x00000001 | // SECRM
		0x00000002 | // UNRM
		0x00000004 | // COMPR
		0x00000008 | // SYNC
		0x00000040 | // NODUMP
		0x00000080 | // NOATIME
		0x00000400 | // NOCOMP
		0x00004000 | // JOURNAL_DATA
		0x00008000 | // NOTAIL
		0x00800000 // NOCOW
	linuxAgentAccessACL = "system.posix_acl_access"
)

type agentFileMetadataSnapshot struct {
	uid              uint32
	gid              uint32
	mode             uint32
	linkCount        uint64
	ctimeSec         int64
	ctimeNsec        int64
	fsxAttr          linuxFSXAttr
	fsxAttrSupported bool
}

func snapshotAgentFileMetadata(file *os.File) (agentFileMetadataSnapshot, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return agentFileMetadataSnapshot{}, err
	}
	fsxAttr, fsxAttrSupported, err := validatedLinuxAgentFileFSXAttr(file)
	if err != nil {
		return agentFileMetadataSnapshot{}, fmt.Errorf("read Linux extended inode attributes: %w", err)
	}
	return agentFileMetadataSnapshot{
		uid:              stat.Uid,
		gid:              stat.Gid,
		mode:             stat.Mode,
		linkCount:        uint64(stat.Nlink),
		ctimeSec:         int64(stat.Ctim.Sec),
		ctimeNsec:        int64(stat.Ctim.Nsec),
		fsxAttr:          fsxAttr,
		fsxAttrSupported: fsxAttrSupported,
	}, nil
}

func sameAgentFileMetadata(a, b agentFileMetadataSnapshot) bool {
	return a == b
}

func sameAgentFileMetadataAcrossExchange(a, b agentFileMetadataSnapshot) bool {
	// renameat2(RENAME_EXCHANGE) updates both exchanged inodes' ctime. Mask only
	// that kernel-owned timestamp so every other captured metadata field still
	// has to match the locked pre-publication snapshot.
	a.ctimeSec, a.ctimeNsec = 0, 0
	b.ctimeSec, b.ctimeNsec = 0, 0
	return a == b
}

func createAgentReplacementFile(locked *lockedAgentFile) (*os.File, string, os.FileInfo, error) {
	if err := preflightAgentSourceExtendedAttributes(locked.file); err != nil {
		return nil, "", nil, err
	}
	sourceFlags, flagsSupported, err := validatedLinuxAgentSourceFlags(locked.file)
	if err != nil {
		// Reject content-authenticating or publication-blocking source state
		// before even an empty candidate is created.
		return nil, "", nil, err
	}
	return createLinuxAgentPrivateFile(
		locked,
		sourceFlags,
		flagsSupported,
		locked.metadata.fsxAttr,
		locked.metadata.fsxAttrSupported,
	)
}

// createAgentRecoveryFile allocates an access-safe inode for the already-locked
// original bytes. It does not copy or validate replacement-only xattrs or inode
// flags: a metadata race that caused post-publication refusal must not prevent a
// mode-000 recovery copy from retaining those original bytes under a name.
func createAgentRecoveryFile(locked *lockedAgentFile) (*os.File, string, os.FileInfo, error) {
	return createLinuxAgentPrivateFile(locked, 0, false, linuxFSXAttr{}, false)
}

func createLinuxAgentPrivateFile(
	locked *lockedAgentFile,
	sourceFlags int,
	flagsSupported bool,
	sourceFSXAttr linuxFSXAttr,
	fsxAttrSupported bool,
) (*os.File, string, os.FileInfo, error) {
	dir := filepath.Dir(locked.path)
	for attempt := 0; attempt < 64; attempt++ {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return nil, "", nil, fmt.Errorf("generate replacement name: %w", err)
		}
		path := filepath.Join(dir, ".bv-replace-"+hex.EncodeToString(random[:]))
		fd, err := unix.Open(path, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return nil, "", nil, fmt.Errorf("create secure replacement: %w", err)
		}
		file := os.NewFile(uintptr(fd), path)
		openedInfo, err := file.Stat()
		if err != nil {
			// Without a handle-derived identity, pathname cleanup could delete a
			// peer replacement. Leave this empty mode-000 artifact for inspection
			// and surface the allocated name in the diagnostic.
			return nil, "", nil, closeUnixAgentPrivateFileAfterFailure(file, path, nil, fmt.Errorf("stat secure replacement: %w", err))
		}
		fail := func(operationErr error) (*os.File, string, os.FileInfo, error) {
			return nil, "", nil, closeUnixAgentPrivateFileAfterFailure(file, path, openedInfo, operationErr)
		}
		pathInfo, err := os.Lstat(path)
		if err != nil {
			return fail(fmt.Errorf("stat secure replacement path: %w", err))
		}
		if !pathInfo.Mode().IsRegular() || !os.SameFile(openedInfo, pathInfo) {
			return fail(fmt.Errorf("secure replacement path changed during creation"))
		}
		if openedInfo.Mode().Perm() != 0 || pathInfo.Mode().Perm() != 0 {
			return fail(fmt.Errorf("secure replacement mode is not 000"))
		}
		if openedInfo.Size() != 0 || pathInfo.Size() != 0 {
			return fail(fmt.Errorf("secure replacement is not empty"))
		}
		if flagsSupported {
			// NOCOW and compression flags affect allocation at write time. Apply
			// every transferable source flag while the inode is still empty so
			// already-written extents cannot retain the wrong semantics.
			if err := applyLinuxAgentCopyableFlags(file, sourceFlags); err != nil {
				return fail(err)
			}
		}
		if fsxAttrSupported {
			// Project quota accounting and extent-allocation hints take effect as
			// blocks are allocated. Preserve the complete settable FSXATTR policy
			// while the candidate is empty, before any replacement byte is written.
			if err := applyLinuxAgentFSXAttr(file, sourceFSXAttr); err != nil {
				return fail(err)
			}
		}
		preparedInfo, err := file.Stat()
		if err != nil {
			return fail(fmt.Errorf("restat secure replacement: %w", err))
		}
		pathInfo, err = os.Lstat(path)
		if err != nil || !pathInfo.Mode().IsRegular() || !os.SameFile(preparedInfo, pathInfo) {
			if err != nil {
				return fail(fmt.Errorf("restat secure replacement path: %w", err))
			}
			return fail(fmt.Errorf("secure replacement path changed while applying pre-write flags"))
		}
		if preparedInfo.Mode().Perm() != 0 || pathInfo.Mode().Perm() != 0 || preparedInfo.Size() != 0 || pathInfo.Size() != 0 {
			return fail(fmt.Errorf("secure replacement became accessible or nonempty while applying pre-write flags"))
		}
		return file, path, preparedInfo, nil
	}
	return nil, "", nil, fmt.Errorf("could not allocate a unique replacement name")
}

func copyAgentPlatformFileFlags(source, replacement *os.File) error {
	sourceFlags, supported, err := validatedLinuxAgentSourceFlags(source)
	if err != nil {
		return err
	}
	if supported {
		if err := verifyLinuxAgentCopyableFlags(replacement, sourceFlags); err != nil {
			return err
		}
	}
	return verifyLinuxAgentFSXAttr(source, replacement)
}

// linuxFSXAttr is the fixed-width Linux UAPI structure used by
// FS_IOC_FSGETXATTR/FS_IOC_FSSETXATTR. fsx_projid is not a named extended
// attribute and is therefore invisible to the generic xattr copy/verification
// path in file_lock_unix.go.
type linuxFSXAttr struct {
	xflags     uint32
	extsize    uint32
	nextents   uint32
	projectID  uint32
	cowextsize uint32
	pad        [8]byte
}

func linuxFSXAttrIoctlRequests() (get, set uint) {
	// Linux's generic and legacy ioctl encodings use opposite direction bits.
	// FS_IOC_GETFLAGS/SETFLAGS are generated by x/sys for every Linux target, so
	// reuse their architecture-correct direction bits while substituting the
	// fsxattr payload size, type ('X'), and operation numbers.
	const directionMask = uint(0xc0000000)
	payload := uint(unsafe.Sizeof(linuxFSXAttr{}))<<16 | uint('X')<<8
	return uint(unix.FS_IOC_GETFLAGS)&directionMask | payload | 31,
		uint(unix.FS_IOC_SETFLAGS)&directionMask | payload | 32
}

func linuxAgentFSXAttrIoctl(file *os.File, request uint, value *linuxFSXAttr) error {
	if file == nil || value == nil {
		return fmt.Errorf("Linux FSXATTR ioctl requires a live file and value")
	}
	_, _, errno := unix.Syscall(
		unix.SYS_IOCTL,
		file.Fd(),
		uintptr(request),
		uintptr(unsafe.Pointer(value)),
	)
	runtime.KeepAlive(file)
	runtime.KeepAlive(value)
	if errno != 0 {
		return errno
	}
	return nil
}

var callLinuxAgentFSXAttrIoctl = linuxAgentFSXAttrIoctl

func linuxFSXAttrUnsupported(err error) bool {
	return linuxFileFlagsUnsupported(err)
}

func linuxAgentFileFSXAttr(file *os.File) (linuxFSXAttr, bool, error) {
	var value linuxFSXAttr
	getRequest, _ := linuxFSXAttrIoctlRequests()
	if err := callLinuxAgentFSXAttrIoctl(file, getRequest, &value); err != nil {
		if linuxFSXAttrUnsupported(err) {
			return linuxFSXAttr{}, false, nil
		}
		return linuxFSXAttr{}, false, err
	}
	return value, true, nil
}

func validateLinuxAgentFSXAttr(value linuxFSXAttr) error {
	if unknown := value.xflags &^ linuxAgentFSXKnownFlags; unknown != 0 {
		return fmt.Errorf("Linux FSXATTR contains unknown xflags %#x", unknown)
	}
	if readOnly := value.xflags & linuxAgentFSXReadOnlyFlags; readOnly != 0 {
		return fmt.Errorf("Linux FSXATTR contains read-only xflags %#x that cannot be recreated", readOnly)
	}
	if directoryOnly := value.xflags & linuxAgentFSXDirectoryOnlyFlags; directoryOnly != 0 {
		return fmt.Errorf("regular agent file contains directory-only Linux xflags %#x", directoryOnly)
	}
	if value.xflags&(linuxFSXFlagImmutable|linuxFSXFlagAppend) != 0 {
		return fmt.Errorf("refusing to replace immutable or append-only Linux agent file")
	}
	if value.extsize != 0 && value.xflags&linuxFSXFlagExtentSize == 0 {
		return fmt.Errorf("Linux FSXATTR has an extent-size value without the extent-size validity flag")
	}
	if value.cowextsize != 0 && value.xflags&linuxFSXFlagCoWExtentSize == 0 {
		return fmt.Errorf("Linux FSXATTR has a CoW extent-size value without the CoW validity flag")
	}
	if value.pad != [8]byte{} {
		return fmt.Errorf("Linux FSXATTR contains nonzero reserved state")
	}
	return nil
}

func validatedLinuxAgentFileFSXAttr(file *os.File) (linuxFSXAttr, bool, error) {
	value, supported, err := linuxAgentFileFSXAttr(file)
	if err != nil || !supported {
		return linuxFSXAttr{}, supported, err
	}
	if err := validateLinuxAgentFSXAttr(value); err != nil {
		return linuxFSXAttr{}, true, err
	}
	return value, true, nil
}

func sameLinuxAgentFSXAttrPolicy(a, b linuxFSXAttr) bool {
	return a.xflags == b.xflags &&
		a.extsize == b.extsize &&
		a.projectID == b.projectID &&
		a.cowextsize == b.cowextsize
}

func linuxAgentFileProjectID(file *os.File) (uint32, bool, error) {
	value, supported, err := validatedLinuxAgentFileFSXAttr(file)
	if err != nil || !supported {
		return 0, supported, err
	}
	return value.projectID, true, nil
}

func applyLinuxAgentFSXAttr(replacement *os.File, source linuxFSXAttr) error {
	if err := validateLinuxAgentFSXAttr(source); err != nil {
		return fmt.Errorf("validate source Linux FSXATTR before applying it: %w", err)
	}
	current, supported, err := validatedLinuxAgentFileFSXAttr(replacement)
	if err != nil {
		return fmt.Errorf("read replacement Linux FSXATTR: %w", err)
	}
	if !supported {
		return fmt.Errorf("replacement filesystem stopped supporting Linux FSXATTR metadata")
	}
	desired := source
	desired.nextents = 0 // fsx_nextents is explicitly get-only in the Linux UAPI.
	desired.pad = [8]byte{}
	if !sameLinuxAgentFSXAttrPolicy(current, desired) {
		_, setRequest := linuxFSXAttrIoctlRequests()
		if err := callLinuxAgentFSXAttrIoctl(replacement, setRequest, &desired); err != nil {
			return fmt.Errorf("preserve Linux FSXATTR policy before writing replacement bytes: %w", err)
		}
	}
	actual, supported, err := validatedLinuxAgentFileFSXAttr(replacement)
	if err != nil {
		return fmt.Errorf("verify replacement Linux FSXATTR policy: %w", err)
	}
	if !supported || !sameLinuxAgentFSXAttrPolicy(source, actual) {
		return fmt.Errorf("replacement Linux FSXATTR policy differs from source")
	}
	return nil
}

func verifyLinuxAgentFSXAttr(source, replacement *os.File) error {
	sourceValue, sourceSupported, err := validatedLinuxAgentFileFSXAttr(source)
	if err != nil {
		return fmt.Errorf("read source Linux FSXATTR policy: %w", err)
	}
	replacementValue, replacementSupported, err := validatedLinuxAgentFileFSXAttr(replacement)
	if err != nil {
		return fmt.Errorf("read replacement Linux FSXATTR policy: %w", err)
	}
	if sourceSupported != replacementSupported {
		return fmt.Errorf("source and replacement disagree on Linux FSXATTR support")
	}
	if sourceSupported && !sameLinuxAgentFSXAttrPolicy(sourceValue, replacementValue) {
		return fmt.Errorf("replacement Linux FSXATTR policy differs from source")
	}
	return nil
}

type finalizedAgentFileMetadataSnapshot struct {
	metadata        agentFileMetadataSnapshot
	inodeFlags      int
	flagsSupported  bool
	extendedAttrs   []agentExtendedAttributeSnapshot
	xattrsSupported bool
}

func snapshotFinalizedAgentFileMetadata(file *os.File) (finalizedAgentFileMetadataSnapshot, error) {
	before, err := snapshotAgentFileMetadata(file)
	if err != nil {
		return finalizedAgentFileMetadataSnapshot{}, err
	}
	inodeFlags, flagsSupported, err := linuxAgentFileInodeFlags(file)
	if err != nil {
		return finalizedAgentFileMetadataSnapshot{}, fmt.Errorf("snapshot Linux inode flags: %w", err)
	}
	extendedAttrs, xattrsSupported, err := snapshotAgentExtendedAttributes(file)
	if err != nil {
		return finalizedAgentFileMetadataSnapshot{}, err
	}
	after, err := snapshotAgentFileMetadata(file)
	if err != nil {
		return finalizedAgentFileMetadataSnapshot{}, err
	}
	if !sameAgentFileMetadata(before, after) {
		return finalizedAgentFileMetadataSnapshot{}, fmt.Errorf("finalized Linux metadata changed while it was snapshotted")
	}
	return finalizedAgentFileMetadataSnapshot{
		metadata:        after,
		inodeFlags:      inodeFlags,
		flagsSupported:  flagsSupported,
		extendedAttrs:   extendedAttrs,
		xattrsSupported: xattrsSupported,
	}, nil
}

func sameFinalizedAgentFileMetadata(a, b finalizedAgentFileMetadataSnapshot) bool {
	return sameAgentFileMetadata(a.metadata, b.metadata) &&
		a.flagsSupported == b.flagsSupported &&
		(!a.flagsSupported || a.inodeFlags == b.inodeFlags) &&
		sameAgentExtendedAttributeSnapshot(a.extendedAttrs, a.xattrsSupported, b.extendedAttrs, b.xattrsSupported)
}

func agentExtendedAttributeDefersAccess(name string) bool {
	return name == linuxAgentAccessACL
}

func validatedLinuxAgentSourceFlags(source *os.File) (int, bool, error) {
	sourceFlags, supported, err := linuxAgentFileInodeFlags(source)
	if err != nil {
		return 0, false, fmt.Errorf("read source inode flags: %w", err)
	}
	if !supported {
		return 0, false, nil
	}
	if sourceFlags&(linuxFSImmutableFlag|linuxFSAppendFlag|linuxFSVerityFlag) != 0 {
		// Atomic rename cannot replace an immutable/append-only source, and
		// applying either flag to the temp would also make safe cleanup fail.
		// Verity authenticates the original inode's contents and cannot be
		// transferred to a replacement inode with FS_IOC_SETFLAGS, so replacing
		// such a file would silently discard its integrity protection.
		return 0, true, fmt.Errorf("refusing to replace immutable, append-only, or verity-protected agent file")
	}
	return sourceFlags, true, nil
}

func linuxAgentFileInodeFlags(file *os.File) (int, bool, error) {
	flags, err := unix.IoctlGetInt(int(file.Fd()), unix.FS_IOC_GETFLAGS)
	if linuxFileFlagsUnsupported(err) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return flags, true, nil
}

func applyLinuxAgentCopyableFlags(replacement *os.File, sourceFlags int) error {
	replacementFlags, err := unix.IoctlGetInt(int(replacement.Fd()), unix.FS_IOC_GETFLAGS)
	if err != nil {
		return fmt.Errorf("read replacement inode flags: %w", err)
	}
	desiredFlags := (replacementFlags &^ linuxAgentCopyableFlags) | (sourceFlags & linuxAgentCopyableFlags)
	if desiredFlags != replacementFlags {
		// Despite the UAPI ioctl encoding, FS_IOC_SETFLAGS takes an int pointer.
		// x/sys deliberately supplies an int32-sized object, which is required on
		// 64-bit big-endian Linux as well as the usual little-endian targets.
		if err := unix.IoctlSetPointerInt(int(replacement.Fd()), unix.FS_IOC_SETFLAGS, desiredFlags); err != nil {
			return fmt.Errorf("preserve inode flags: %w", err)
		}
	}
	return verifyLinuxAgentCopyableFlags(replacement, sourceFlags)
}

func verifyLinuxAgentCopyableFlags(replacement *os.File, sourceFlags int) error {
	actualFlags, err := unix.IoctlGetInt(int(replacement.Fd()), unix.FS_IOC_GETFLAGS)
	if err != nil {
		return fmt.Errorf("verify replacement inode flags: %w", err)
	}
	if actualFlags&linuxAgentCopyableFlags != sourceFlags&linuxAgentCopyableFlags {
		return fmt.Errorf("replacement inode flags differ from source")
	}
	return nil
}

func finalizeAgentReplacementAccess(source, replacement *os.File, mode os.FileMode) error {
	if err := verifyUnixAgentReplacementPrivateMetadata(source, replacement); err != nil {
		return err
	}
	sourceACL, sourceHasACL, err := linuxAgentFileAccessACL(source)
	if err != nil {
		return fmt.Errorf("read source access ACL before finalization: %w", err)
	}
	_, replacementHasACL, err := linuxAgentFileAccessACL(replacement)
	if err != nil {
		return fmt.Errorf("inspect private replacement access ACL: %w", err)
	}
	if replacementHasACL {
		return fmt.Errorf("private replacement unexpectedly has an access ACL before finalization")
	}
	if sourceHasACL {
		// Setting the exact source ACL can update st_mode. Do it only after every
		// private preparation step has succeeded; the resulting policy is no
		// broader than the locked source even if a later verification fails.
		if err := unix.Fsetxattr(int(replacement.Fd()), linuxAgentAccessACL, sourceACL, 0); err != nil {
			return fmt.Errorf("preserve source access ACL: %w", err)
		}
	}
	// Chown can clear set-ID bits, so restore the complete source mode after the
	// ACL. Chmod uses the same group-class mask already encoded by the source ACL.
	if err := replacement.Chmod(mode); err != nil {
		return fmt.Errorf("preserve mode: %w", err)
	}
	if err := verifyUnixAgentReplacementMetadata(source, replacement, true); err != nil {
		return err
	}
	sourceFlags, supported, err := validatedLinuxAgentSourceFlags(source)
	if err != nil {
		return err
	}
	if supported {
		if err := verifyLinuxAgentCopyableFlags(replacement, sourceFlags); err != nil {
			return err
		}
	}
	if err := verifyLinuxAgentFSXAttr(source, replacement); err != nil {
		return err
	}
	currentSourceACL, currentSourceHasACL, err := linuxAgentFileAccessACL(source)
	if err != nil {
		return fmt.Errorf("recheck source access ACL: %w", err)
	}
	if sourceHasACL != currentSourceHasACL || !bytes.Equal(sourceACL, currentSourceACL) {
		return fmt.Errorf("source access ACL changed during replacement finalization")
	}
	replacementACL, replacementHasACL, err := linuxAgentFileAccessACL(replacement)
	if err != nil {
		return fmt.Errorf("verify replacement access ACL: %w", err)
	}
	if currentSourceHasACL != replacementHasACL || !bytes.Equal(currentSourceACL, replacementACL) {
		return fmt.Errorf("replacement access ACL differs from source")
	}
	return nil
}

func verifyFinalizedAgentReplacementMetadata(source, replacement *os.File) error {
	before, err := snapshotAgentFileMetadata(replacement)
	if err != nil {
		return fmt.Errorf("snapshot finalized replacement metadata: %w", err)
	}
	if err := verifyUnixAgentReplacementMetadata(source, replacement, true); err != nil {
		return err
	}
	sourceFlags, supported, err := validatedLinuxAgentSourceFlags(source)
	if err != nil {
		return err
	}
	if supported {
		replacementFlags, err := unix.IoctlGetInt(int(replacement.Fd()), unix.FS_IOC_GETFLAGS)
		if err != nil {
			return fmt.Errorf("read finalized replacement inode flags: %w", err)
		}
		if replacementFlags&(linuxFSImmutableFlag|linuxFSAppendFlag|linuxFSVerityFlag) != 0 {
			return fmt.Errorf("refusing to publish immutable, append-only, or verity-protected replacement candidate")
		}
		if err := verifyLinuxAgentCopyableFlags(replacement, sourceFlags); err != nil {
			return err
		}
	}
	if err := verifyLinuxAgentFSXAttr(source, replacement); err != nil {
		return err
	}
	if err := verifyAgentExtendedAttributes(source, replacement); err != nil {
		return err
	}
	after, err := snapshotAgentFileMetadata(replacement)
	if err != nil {
		return fmt.Errorf("resnapshot finalized replacement metadata: %w", err)
	}
	if !sameAgentFileMetadata(before, after) {
		return fmt.Errorf("finalized replacement metadata changed during verification")
	}
	return nil
}

func linuxAgentFileAccessACL(file *os.File) ([]byte, bool, error) {
	size, err := unix.Fgetxattr(int(file.Fd()), linuxAgentAccessACL, nil)
	if errors.Is(err, unix.ENODATA) || errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EOPNOTSUPP) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	value := make([]byte, size)
	if size == 0 {
		return value, true, nil
	}
	read, err := unix.Fgetxattr(int(file.Fd()), linuxAgentAccessACL, value)
	if err != nil {
		return nil, false, err
	}
	return value[:read], true, nil
}

func linuxFileFlagsUnsupported(err error) bool {
	return errors.Is(err, unix.ENOTTY) || errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EOPNOTSUPP)
}

func makeAgentReplacementRemovable(_ *os.File) error {
	return nil
}

func makeAgentDisplacedSourcePrivate(file *os.File) error {
	if file == nil {
		return fmt.Errorf("displaced source handle is unavailable")
	}
	_, hasAccessACL, err := linuxAgentFileAccessACL(file)
	if err != nil {
		return fmt.Errorf("inspect displaced source access ACL: %w", err)
	}
	if hasAccessACL {
		if err := unix.Fremovexattr(int(file.Fd()), linuxAgentAccessACL); err != nil && !errors.Is(err, unix.ENODATA) {
			return fmt.Errorf("remove displaced source access ACL: %w", err)
		}
	}
	if err := file.Chmod(0); err != nil {
		return fmt.Errorf("make displaced source mode 000: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync private displaced source: %w", err)
	}
	return verifyAgentDisplacedSourcePrivate(file)
}

func verifyAgentDisplacedSourcePrivate(file *os.File) error {
	if file == nil {
		return fmt.Errorf("displaced source handle is unavailable")
	}
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat private displaced source: %w", err)
	}
	if info.Mode().Perm() != 0 {
		return fmt.Errorf("private displaced source mode is %o, want 000", info.Mode().Perm())
	}
	_, hasAccessACL, err := linuxAgentFileAccessACL(file)
	if err != nil {
		return fmt.Errorf("verify private displaced source access ACL: %w", err)
	}
	if hasAccessACL {
		return fmt.Errorf("private displaced source still has an access ACL")
	}
	return verifyAgentFileHasSingleLink(file)
}

func publishAgentFileExclusive(sourcePath, destinationPath string) (bool, error) {
	err := unix.Renameat2(unix.AT_FDCWD, sourcePath, unix.AT_FDCWD, destinationPath, unix.RENAME_NOREPLACE)
	if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL) || errors.Is(err, unix.EOPNOTSUPP) {
		return false, fmt.Errorf("atomic no-replace rename is unsupported: %w", err)
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func exchangeAgentFilePaths(sourcePath, destinationPath string) error {
	err := unix.Renameat2(unix.AT_FDCWD, sourcePath, unix.AT_FDCWD, destinationPath, unix.RENAME_EXCHANGE)
	if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL) || errors.Is(err, unix.EOPNOTSUPP) {
		return fmt.Errorf("atomic exchange rename is unsupported: %w", err)
	}
	if err != nil {
		return fmt.Errorf("atomic exchange rename: %w", err)
	}
	return nil
}
