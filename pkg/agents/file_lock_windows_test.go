//go:build windows

package agents

import (
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestAgentFileMutexContentionIsBounded(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "AGENTS.md")
	if err := os.WriteFile(filePath, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	locked, err := lockAgentFileForMutation(filePath)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.close()

	result := make(chan error, 1)
	go func() {
		file, unlock, err := openAndLockAgentFileForMutation(filePath, 40*time.Millisecond)
		if unlock != nil {
			_ = unlock()
		}
		if file != nil {
			_ = file.Close()
		}
		result <- err
	}()
	if err := <-result; !errors.Is(err, errAgentFileBusy) {
		t.Fatalf("contended mutex error=%v, want busy", err)
	}
}

func TestAgentFilePathMutexSpansReplacementIdentityRollover(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(filePath, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	locked, err := lockAgentFileForMutation(filePath)
	if err != nil {
		t.Fatal(err)
	}
	sourcePathMutexName, err := windowsAgentPathMutexName(locked.file)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if locked != nil {
			_ = locked.close()
		}
	}()

	originalReplace := replaceAgentFile
	defer func() { replaceAgentFile = originalReplace }()
	published := make(chan struct{})
	allowReturn := make(chan struct{})
	replaceAgentFile = func(destination, replacement, backup *uint16) error {
		if err := simulateSuccessfulWindowsReplaceFile(
			windows.UTF16PtrToString(destination),
			windows.UTF16PtrToString(replacement),
			windows.UTF16PtrToString(backup),
		); err != nil {
			return err
		}
		close(published)
		<-allowReturn
		return nil
	}

	replaceResult := make(chan error, 1)
	go func() {
		replaceResult <- locked.replace([]byte("replacement"))
	}()
	select {
	case <-published:
	case replaceErr := <-replaceResult:
		close(allowReturn)
		t.Fatalf("replacement returned before the injected post-publication pause: %v", replaceErr)
	}

	publishedInfo, err := agentFilePathInfo(filePath)
	if err != nil {
		close(allowReturn)
		<-replaceResult
		t.Fatal(err)
	}
	if os.SameFile(locked.info, publishedInfo) {
		close(allowReturn)
		<-replaceResult
		t.Fatal("replacement fixture did not roll the destination file identity")
	}
	publishedFile, err := openAgentFileForInspection(filePath)
	if err != nil {
		close(allowReturn)
		<-replaceResult
		t.Fatal(err)
	}
	publishedPathMutexName, mutexNameErr := windowsAgentPathMutexName(publishedFile)
	publishedCloseErr := publishedFile.Close()
	if err := errors.Join(mutexNameErr, publishedCloseErr); err != nil {
		close(allowReturn)
		<-replaceResult
		t.Fatal(err)
	}
	if windows.UTF16PtrToString(sourcePathMutexName) != windows.UTF16PtrToString(publishedPathMutexName) {
		close(allowReturn)
		<-replaceResult
		t.Fatal("normalized pathname mutex changed across replacement identity rollover")
	}

	contenderResult := make(chan error, 1)
	go func() {
		file, unlock, err := openAndLockAgentFileForMutation(filePath, 40*time.Millisecond)
		if unlock != nil {
			err = errors.Join(err, unlock())
		}
		if file != nil {
			err = errors.Join(err, file.Close())
		}
		contenderResult <- err
	}()
	contenderErr := <-contenderResult
	close(allowReturn)
	replaceErr := <-replaceResult
	if !errors.Is(contenderErr, errAgentFileBusy) {
		t.Fatalf("post-publication contender error=%v, want busy until original writer releases the pathname lock", contenderErr)
	}
	if replaceErr != nil {
		t.Fatalf("replacement failed after releasing injected publication pause: %v", replaceErr)
	}
	if err := locked.close(); err != nil {
		t.Fatal(err)
	}
	locked = nil

	file, unlock, err := openAndLockAgentFileForMutation(filePath, 200*time.Millisecond)
	if err != nil {
		t.Fatalf("pathname mutex remained locked after writer completion: %v", err)
	}
	if err := errors.Join(unlock(), file.Close()); err != nil {
		t.Fatalf("release post-handoff lock: %v", err)
	}
}

func TestAgentFilePathInfoIsComparableWhileSourceHandleIsOpen(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "AGENTS.md")
	if err := os.WriteFile(filePath, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(filePath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	handleInfo, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	pathInfo, err := agentFilePathInfo(filePath)
	if err != nil {
		t.Fatalf("path identity probe conflicted with open source handle: %v", err)
	}
	if !os.SameFile(handleInfo, pathInfo) {
		t.Fatal("handle and path identity probes disagreed for the same file")
	}
}

func TestCloseWindowsAgentPrivateFileAfterFailureReportsRetainedPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".bv-replace-diagnostic")
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}

	err = closeWindowsAgentPrivateFileAfterFailure(file, path, info, errors.New("injected preparation failure"))
	if err == nil || !strings.Contains(err.Error(), "injected preparation failure") ||
		!strings.Contains(err.Error(), "private replacement retained at") ||
		!strings.Contains(err.Error(), path) {
		t.Fatalf("failure diagnostic=%v, want cause and exact retained path", err)
	}
	retained, statErr := agentFilePathInfo(path)
	if statErr != nil {
		t.Fatalf("retained candidate is not inspectable: %v", statErr)
	}
	if !retained.Mode().IsRegular() || !os.SameFile(info, retained) || retained.Size() != 0 {
		t.Fatalf("retained candidate changed: mode=%v size=%d", retained.Mode(), retained.Size())
	}
}

func TestWindowsAgentPathPointerUsesExtendedLengthSyntax(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "drive", path: `C:\repo\AGENTS.md`, want: `\\?\C:\repo\AGENTS.md`},
		{name: "UNC", path: `\\server\share\AGENTS.md`, want: `\\?\UNC\server\share\AGENTS.md`},
		{name: "already extended", path: `\\?\C:\repo\AGENTS.md`, want: `\\?\C:\repo\AGENTS.md`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pointer, err := windowsAgentPathPointer(tt.path)
			if err != nil {
				t.Fatal(err)
			}
			if got := windows.UTF16PtrToString(pointer); got != tt.want {
				t.Fatalf("normalized path=%q, want %q", got, tt.want)
			}
		})
	}
}

func TestWindowsAgentPathPointerRejectsDeviceNamespaces(t *testing.T) {
	for _, path := range []string{
		`\\.\PhysicalDrive0`,
		`\\?\GLOBALROOT\Device\HarddiskVolumeShadowCopy1\AGENTS.md`,
		`\\?\PIPE\bv-agent-file`,
		`\\?\Volume{00000000-0000-0000-0000-000000000000}\AGENTS.md`,
		`\\?\UNC\.\share\AGENTS.md`,
		`\\?\UNC\server`,
		`\\server\pipe\bv-agent-file`,
		`\\?\UNC\server\PIPE\bv-agent-file`,
		`\\server\mailslot\bv-agent-file`,
		`\\?\UNC\server\IPC$\bv-agent-file`,
	} {
		if pointer, err := windowsAgentPathPointer(path); err == nil {
			t.Fatalf("device or malformed namespace %q produced %q", path, windows.UTF16PtrToString(pointer))
		}
	}
}

func TestAgentFileMutationSupportsExtendedLengthPath(t *testing.T) {
	dir := t.TempDir()
	for len(dir) < 280 {
		dir = filepath.Join(dir, strings.Repeat("long", 8))
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	filePath := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(filePath, []byte("# Original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := AppendBlurbToFile(filePath); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), BlurbStartMarker) {
		t.Fatal("long-path mutation did not publish the managed blurb")
	}
}

func TestWindowsAgentReplacementCreationAttributesPreserveEFSState(t *testing.T) {
	if got := windowsAgentReplacementCreationAttributes(windows.FILE_ATTRIBUTE_ARCHIVE); got != windows.FILE_ATTRIBUTE_NORMAL {
		t.Fatalf("ordinary source creation attributes=%#x, want FILE_ATTRIBUTE_NORMAL", got)
	}
	if got := windowsAgentReplacementCreationAttributes(windows.FILE_ATTRIBUTE_ARCHIVE | windows.FILE_ATTRIBUTE_ENCRYPTED); got != windows.FILE_ATTRIBUTE_ENCRYPTED {
		t.Fatalf("encrypted source creation attributes=%#x, want FILE_ATTRIBUTE_ENCRYPTED", got)
	}
}

func TestWindowsAgentMetadataDetectsNamedStreams(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "AGENTS.md")
	if err := os.WriteFile(filePath, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath+":bv-test", []byte("alternate"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(filePath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	metadata, err := snapshotAgentFileMetadata(file)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.namedStreamCount != 1 {
		t.Fatalf("named stream count=%d, want 1", metadata.namedStreamCount)
	}
}

func TestWindowsReplacementRefusesSourceWithNamedStream(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "AGENTS.md")
	if err := os.WriteFile(filePath, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	streamPath := filePath + ":bv-preserve"
	if err := os.WriteFile(streamPath, []byte("alternate"), 0o600); err != nil {
		t.Fatal(err)
	}
	locked, err := lockAgentFileForMutation(filePath)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.close()

	err = locked.replace([]byte("replacement"))
	if err == nil || !strings.Contains(err.Error(), "alternate data streams") {
		t.Fatalf("replace error=%v, want fail-closed alternate-stream refusal", err)
	}
	content, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "original" {
		t.Fatalf("destination content=%q, want original", content)
	}
	streamContent, err := os.ReadFile(streamPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(streamContent) != "alternate" {
		t.Fatalf("alternate-stream content=%q, want alternate", streamContent)
	}
	candidates, err := filepath.Glob(filepath.Join(filepath.Dir(filePath), ".bv-replace-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 0 {
		t.Fatalf("named-stream preflight allocated replacement candidates: %v", candidates)
	}
}

func TestWindowsExclusiveCreateAlternateStreamRaceIsPartialPublication(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "AGENTS.md")
	originalPublish := publishAgentFileExclusiveForCreate
	defer func() { publishAgentFileExclusiveForCreate = originalPublish }()
	publishAgentFileExclusiveForCreate = func(sourcePath, destinationPath string) (bool, error) {
		published, err := publishAgentFileExclusive(sourcePath, destinationPath)
		if err != nil {
			return published, err
		}
		preparedInfo, err := os.Stat(destinationPath)
		if err != nil {
			return published, err
		}
		if err := os.WriteFile(destinationPath+":bv-race", []byte("injected"), 0o600); err != nil {
			return published, err
		}
		if err := os.Chtimes(destinationPath, preparedInfo.ModTime(), preparedInfo.ModTime()); err != nil {
			return published, err
		}
		return published, nil
	}

	err := writeFileDirectExclusive(filePath, []byte("created"))
	if err == nil || !strings.Contains(err.Error(), "publication is partial") ||
		!strings.Contains(err.Error(), "alternate data streams") {
		t.Fatalf("exclusive-create error=%v, want partial-publication alternate-stream diagnostic", err)
	}
	content, readErr := os.ReadFile(filePath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(content) != "created" {
		t.Fatalf("published content=%q, want created", content)
	}
	streamContent, readErr := os.ReadFile(filePath + ":bv-race")
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(streamContent) != "injected" {
		t.Fatalf("published alternate-stream content=%q, want injected", streamContent)
	}
}

func TestWindowsReplacementMetadataVerificationIncludesExtendedSecurityPolicy(t *testing.T) {
	base := agentFileMetadataSnapshot{linkCount: 1}
	if err := verifyWindowsAgentReplacementMetadataSnapshots(base, base); err != nil {
		t.Fatalf("identical snapshots failed verification: %v", err)
	}

	tests := []struct {
		name       string
		want       string
		mutate     func(*agentFileMetadataSnapshot, *agentFileMetadataSnapshot)
		policyOnly bool
	}{
		{
			name: "source alternate data stream",
			want: "alternate data streams",
			mutate: func(source, _ *agentFileMetadataSnapshot) {
				source.namedStreamCount = 1
			},
		},
		{
			name: "replacement alternate data stream",
			want: "alternate data streams",
			mutate: func(_, replacement *agentFileMetadataSnapshot) {
				replacement.namedStreamCount = 1
			},
		},
		{
			name: "resource attributes",
			want: "resource attributes",
			mutate: func(_, replacement *agentFileMetadataSnapshot) {
				replacement.resourceAttributes[0] = 1
			},
			policyOnly: true,
		},
		{
			name: "central access policy scope",
			want: "central access policy scope",
			mutate: func(_, replacement *agentFileMetadataSnapshot) {
				replacement.centralAccessPolicyScope[0] = 1
			},
			policyOnly: true,
		},
		{
			name: "source encrypted candidate unencrypted",
			want: "EFS encryption state",
			mutate: func(source, _ *agentFileMetadataSnapshot) {
				source.attributes = windows.FILE_ATTRIBUTE_ENCRYPTED
			},
		},
		{
			name: "source unencrypted candidate encrypted",
			want: "EFS encryption state",
			mutate: func(_, replacement *agentFileMetadataSnapshot) {
				replacement.attributes = windows.FILE_ATTRIBUTE_ENCRYPTED
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := base
			replacement := base
			tt.mutate(&source, &replacement)
			if err := verifyWindowsAgentReplacementMetadataSnapshots(source, replacement); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("metadata verification error=%v, want %q mismatch", err, tt.want)
			}
			if tt.policyOnly && sameWindowsAgentAccessPolicy(source, replacement) {
				t.Fatalf("final access-policy comparison ignored changed %s", tt.name)
			}
		})
	}
}

func TestWindowsSecurityComponentsAreCopiedAndReverified(t *testing.T) {
	dir := t.TempDir()
	source, err := os.Create(filepath.Join(dir, "source.md"))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	replacement, err := os.Create(filepath.Join(dir, "replacement.md"))
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	descriptor, err := windows.SecurityDescriptorFromString(`S:(ML;;NW;;;LW)`)
	if err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct {
		name        string
		information windows.SECURITY_INFORMATION
	}{
		{name: "resource attributes", information: windows.ATTRIBUTE_SECURITY_INFORMATION},
		{name: "central access policy scope", information: windows.SCOPE_SECURITY_INFORMATION},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var sourceDigest [sha256.Size]byte
			sourceDigest[0] = 1
			var inheritedDigest [sha256.Size]byte
			inheritedDigest[0] = 2
			copied := false
			replacementQueries := 0
			query := func(file *os.File, information windows.SECURITY_INFORMATION) (*windows.SECURITY_DESCRIPTOR, [sha256.Size]byte, error) {
				if information != tt.information {
					t.Fatalf("queried security information=%#x, want %#x", information, tt.information)
				}
				if file == source {
					return descriptor, sourceDigest, nil
				}
				if file != replacement {
					t.Fatalf("queried unexpected file %v", file)
				}
				replacementQueries++
				if copied {
					return descriptor, sourceDigest, nil
				}
				return descriptor, inheritedDigest, nil
			}
			set := func(
				handle windows.Handle,
				objectType windows.SE_OBJECT_TYPE,
				information windows.SECURITY_INFORMATION,
				_, _ *windows.SID,
				_ *windows.ACL,
				sacl *windows.ACL,
			) error {
				if handle != windows.Handle(replacement.Fd()) || objectType != windows.SE_FILE_OBJECT {
					t.Fatalf("set component on handle=%v type=%v, want replacement file", handle, objectType)
				}
				if information != tt.information {
					t.Fatalf("set security information=%#x, want %#x", information, tt.information)
				}
				if sacl == nil {
					t.Fatal("set component without source SACL")
				}
				copied = true
				return nil
			}
			if err := copyWindowsAgentSecurityComponentUsing(source, replacement, tt.information, tt.name, query, set); err != nil {
				t.Fatal(err)
			}
			if !copied {
				t.Fatalf("%s mismatch was not copied", tt.name)
			}
			if replacementQueries != 2 {
				t.Fatalf("replacement %s queries=%d, want pre-copy and post-copy verification", tt.name, replacementQueries)
			}
		})
	}
}

func TestWindowsScopedPolicyMismatchFailsClosedWithoutPrivilege(t *testing.T) {
	dir := t.TempDir()
	source, err := os.Create(filepath.Join(dir, "source.md"))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	replacement, err := os.Create(filepath.Join(dir, "replacement.md"))
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	descriptor, err := windows.SecurityDescriptorFromString(`S:(ML;;NW;;;LW)`)
	if err != nil {
		t.Fatal(err)
	}
	queryCount := 0
	query := func(_ *os.File, information windows.SECURITY_INFORMATION) (*windows.SECURITY_DESCRIPTOR, [sha256.Size]byte, error) {
		if information != windows.SCOPE_SECURITY_INFORMATION {
			t.Fatalf("queried security information=%#x, want SCOPE_SECURITY_INFORMATION", information)
		}
		var digest [sha256.Size]byte
		digest[0] = byte(queryCount + 1)
		queryCount++
		return descriptor, digest, nil
	}
	set := func(
		windows.Handle,
		windows.SE_OBJECT_TYPE,
		windows.SECURITY_INFORMATION,
		*windows.SID,
		*windows.SID,
		*windows.ACL,
		*windows.ACL,
	) error {
		return windows.ERROR_PRIVILEGE_NOT_HELD
	}
	err = copyWindowsAgentSecurityComponentUsing(
		source,
		replacement,
		windows.SCOPE_SECURITY_INFORMATION,
		"central access policy scope",
		query,
		set,
	)
	if !errors.Is(err, windows.ERROR_PRIVILEGE_NOT_HELD) || !strings.Contains(err.Error(), "central access policy scope") {
		t.Fatalf("scope-copy error=%v, want privilege failure with component context", err)
	}
}

func TestEncryptedReplacementIsProtectedBeforeFirstWrite(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "AGENTS.md")
	fileName, err := windowsAgentPathPointer(filePath)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(
		fileName,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		0,
		nil,
		windows.CREATE_NEW,
		windows.FILE_ATTRIBUTE_ENCRYPTED,
		0,
	)
	if err != nil {
		if windowsEFSEnvironmentUnavailable(err) {
			t.Skipf("EFS is unavailable in this test environment: %v", err)
		}
		t.Fatalf("create encrypted source fixture: %v", err)
	}
	source := os.NewFile(uintptr(handle), filePath)
	written, writeErr := source.Write([]byte("original"))
	closeErr := source.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		t.Fatalf("write encrypted source fixture: %v", err)
	}
	if written != len("original") {
		t.Fatalf("encrypted source write=%d bytes, want %d", written, len("original"))
	}

	locked, err := lockAgentFileForMutation(filePath)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.close()
	replacement, _, replacementInfo, err := createAgentReplacementFile(locked)
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	if replacementInfo.Size() != 0 {
		t.Fatalf("encrypted candidate size=%d, want zero before first write", replacementInfo.Size())
	}
	var replacementHandleInfo windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(replacement.Fd()), &replacementHandleInfo); err != nil {
		t.Fatal(err)
	}
	if replacementHandleInfo.FileAttributes&windows.FILE_ATTRIBUTE_ENCRYPTED == 0 {
		t.Fatalf("empty candidate attributes=%#x, want FILE_ATTRIBUTE_ENCRYPTED", replacementHandleInfo.FileAttributes)
	}
	if err := verifyWindowsAgentReplacementMetadata(locked.file, replacement); err != nil {
		t.Fatalf("encrypted empty candidate failed metadata parity: %v", err)
	}
}

func windowsEFSEnvironmentUnavailable(err error) bool {
	return errors.Is(err, windows.ERROR_ACCESS_DENIED) ||
		errors.Is(err, windows.ERROR_INVALID_FUNCTION) ||
		errors.Is(err, windows.ERROR_NOT_SUPPORTED) ||
		errors.Is(err, windows.ERROR_FILE_SYSTEM_LIMITATION) ||
		errors.Is(err, windows.ERROR_ENCRYPTION_FAILED) ||
		errors.Is(err, windows.ERROR_NO_RECOVERY_POLICY) ||
		errors.Is(err, windows.ERROR_NO_EFS) ||
		errors.Is(err, windows.ERROR_WRONG_EFS) ||
		errors.Is(err, windows.ERROR_NO_USER_KEYS) ||
		errors.Is(err, windows.ERROR_DIR_EFS_DISALLOWED) ||
		errors.Is(err, windows.ERROR_EFS_SERVER_NOT_TRUSTED) ||
		errors.Is(err, windows.ERROR_BAD_RECOVERY_POLICY) ||
		errors.Is(err, windows.ERROR_VOLUME_NOT_SUPPORT_EFS) ||
		errors.Is(err, windows.ERROR_EFS_DISABLED) ||
		errors.Is(err, windows.ERROR_EFS_VERSION_NOT_SUPPORT)
}

func TestWindowsMetadataVerificationRejectsHardLinkedCandidate(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.md")
	replacementPath := filepath.Join(dir, "replacement.md")
	if err := os.WriteFile(sourcePath, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(replacementPath, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(replacementPath, filepath.Join(dir, "replacement-alias.md")); err != nil {
		t.Skipf("filesystem does not support hard-link regression fixture: %v", err)
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	replacement, err := os.Open(replacementPath)
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	if err := verifyWindowsAgentReplacementMetadata(source, replacement); err == nil || !strings.Contains(err.Error(), "hard links") {
		t.Fatalf("metadata verification error=%v, want candidate hard-link refusal", err)
	}
}

func TestReplacementHandleDeniesPeerReadWriteAndDeleteSharing(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(filePath, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	protectAgentFileDACLForCurrentUser(t, filePath)
	locked, err := lockAgentFileForMutation(filePath)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.close()
	replacement, replacementPath, replacementInfo, err := createAgentReplacementFile(locked)
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	pathInfo, err := agentFilePathInfo(replacementPath)
	if err != nil {
		t.Fatalf("metadata-only identity probe conflicted with no-share replacement handle: %v", err)
	}
	if !os.SameFile(replacementInfo, pathInfo) {
		t.Fatal("metadata-only identity probe disagreed with the live replacement handle")
	}
	if replacementInfo.Size() != 0 {
		t.Fatalf("newly secured replacement size=%d, want empty", replacementInfo.Size())
	}
	if err := verifyWindowsAgentReplacementMetadata(locked.file, replacement); err != nil {
		t.Fatalf("replacement was not secured like its source before the first write: %v", err)
	}

	peerRead, err := os.Open(replacementPath)
	if peerRead != nil {
		_ = peerRead.Close()
	}
	if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		t.Fatalf("peer read-open error=%v, want Windows sharing violation", err)
	}

	peer, err := os.OpenFile(replacementPath, os.O_WRONLY, 0)
	if peer != nil {
		_ = peer.Close()
	}
	if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		t.Fatalf("peer write-open error=%v, want Windows sharing violation", err)
	}
	if err := os.Rename(replacementPath, filepath.Join(dir, "peer-delete")); err == nil {
		t.Fatal("peer renamed replacement despite delete sharing being denied")
	}
	if _, err := os.Lstat(replacementPath); err != nil {
		t.Fatalf("replacement path disappeared after denied peer rename: %v", err)
	}
}

func TestWriteReplacementPreservesProtectedDACL(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "AGENTS.md")
	if err := os.WriteFile(filePath, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	protectAgentFileDACLForCurrentUser(t, filePath)
	before, err := windows.GetNamedSecurityInfo(
		filePath,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.OWNER_SECURITY_INFORMATION|windows.GROUP_SECURITY_INFORMATION,
	)
	if err != nil {
		t.Fatal(err)
	}

	locked, err := lockAgentFileForMutation(filePath)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.close()
	if err := locked.replace([]byte("replacement")); err != nil {
		t.Fatal(err)
	}

	after, err := windows.GetNamedSecurityInfo(
		filePath,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.OWNER_SECURITY_INFORMATION|windows.GROUP_SECURITY_INFORMATION,
	)
	if err != nil {
		t.Fatal(err)
	}
	if before.String() != after.String() {
		t.Fatalf("security descriptor changed:\n before: %s\n  after: %s", before.String(), after.String())
	}
}

func protectAgentFileDACLForCurrentUser(t *testing.T, filePath string) {
	t.Helper()
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		t.Fatal(err)
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.GRANT_ACCESS,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_USER,
			TrusteeValue: windows.TrusteeValueFromSID(user.User.Sid),
		},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(
		filePath,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		acl,
		nil,
	); err != nil {
		t.Fatal(err)
	}
}

func TestReplaceFileFailureRetainsCompleteRecoveryFile(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(filePath, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	locked, err := lockAgentFileForMutation(filePath)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.close()

	originalReplace := replaceAgentFile
	defer func() { replaceAgentFile = originalReplace }()
	replaceAgentFile = func(destination, _ *uint16, backup *uint16) error {
		if err := os.Rename(windows.UTF16PtrToString(destination), windows.UTF16PtrToString(backup)); err != nil {
			return err
		}
		return windows.ERROR_UNABLE_TO_MOVE_REPLACEMENT_2
	}

	err = locked.replace([]byte("complete replacement"))
	if err == nil || !strings.Contains(err.Error(), "original retained") || !strings.Contains(err.Error(), "replacement retained") {
		t.Fatalf("replace error=%v, want original and replacement recovery diagnostics", err)
	}
	matches, err := filepath.Glob(filepath.Join(dir, ".bv-replace-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("recovery replacements=%v, want exactly one", matches)
	}
	content, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "complete replacement" {
		t.Fatalf("recovery content=%q, want complete replacement", content)
	}
	backups, err := filepath.Glob(filepath.Join(dir, ".bv-backup-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 {
		t.Fatalf("recovery backups=%v, want exactly one", backups)
	}
	original, err := os.ReadFile(backups[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(original) != "original" {
		t.Fatalf("displaced original=%q", original)
	}
}

func TestReplaceFilePartialFailuresNeverClaimOriginalPathIsSafe(t *testing.T) {
	for _, tt := range []struct {
		name           string
		replaceErr     error
		moveToBackup   bool
		wantDiagnostic string
	}{
		{name: "1176 preserves destination with named backup", replaceErr: windows.ERROR_UNABLE_TO_MOVE_REPLACEMENT, wantDiagnostic: "original destination preserved"},
		{name: "1177 retains original at named backup", replaceErr: windows.ERROR_UNABLE_TO_MOVE_REPLACEMENT_2, moveToBackup: true, wantDiagnostic: "original retained"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			filePath := filepath.Join(dir, "AGENTS.md")
			if err := os.WriteFile(filePath, []byte("original"), 0o600); err != nil {
				t.Fatal(err)
			}
			locked, err := lockAgentFileForMutation(filePath)
			if err != nil {
				t.Fatal(err)
			}
			defer locked.close()

			originalReplace := replaceAgentFile
			defer func() { replaceAgentFile = originalReplace }()
			replaceAgentFile = func(destination, _ *uint16, backup *uint16) error {
				if tt.moveToBackup {
					if err := os.Rename(windows.UTF16PtrToString(destination), windows.UTF16PtrToString(backup)); err != nil {
						return err
					}
				}
				return tt.replaceErr
			}

			err = locked.replace([]byte("replacement"))
			if err == nil || !strings.Contains(err.Error(), tt.wantDiagnostic) || !strings.Contains(err.Error(), "replacement retained") {
				t.Fatalf("replace error=%v, want %q and retained-candidate diagnostics", err, tt.wantDiagnostic)
			}
			matches, globErr := filepath.Glob(filepath.Join(dir, ".bv-replace-*"))
			if globErr != nil {
				t.Fatal(globErr)
			}
			if len(matches) != 1 {
				t.Fatalf("retained replacements=%v, want exactly one", matches)
			}
		})
	}
}

func TestNamedBackupIsRemovedByIdentityAfterSuccessfulReplacement(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(filePath, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	locked, err := lockAgentFileForMutation(filePath)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.close()

	originalReplace := replaceAgentFile
	defer func() { replaceAgentFile = originalReplace }()
	replaceAgentFile = func(destination, replacement, backup *uint16) error {
		destinationPath := windows.UTF16PtrToString(destination)
		replacementPath := windows.UTF16PtrToString(replacement)
		backupPath := windows.UTF16PtrToString(backup)
		return simulateSuccessfulWindowsReplaceFile(destinationPath, replacementPath, backupPath)
	}

	if err := locked.replace([]byte("replacement")); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "replacement" {
		t.Fatalf("published content=%q, want replacement", content)
	}
	fileName, err := windowsAgentPathPointer(filePath)
	if err != nil {
		t.Fatal(err)
	}
	attributes, err := windows.GetFileAttributes(fileName)
	if err != nil {
		t.Fatal(err)
	}
	if attributes&windows.FILE_ATTRIBUTE_TEMPORARY != 0 {
		t.Fatalf("published replacement retained FILE_ATTRIBUTE_TEMPORARY: %#x", attributes)
	}
	backups, err := filepath.Glob(filepath.Join(dir, ".bv-backup-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 0 {
		t.Fatalf("successful replacement retained backups: %v", backups)
	}
}

func TestReadOnlySourceAttributesVerifyAfterSuccessfulReplacement(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(filePath, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	fileName, err := windowsAgentPathPointer(filePath)
	if err != nil {
		t.Fatal(err)
	}
	attributes, err := windows.GetFileAttributes(fileName)
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetFileAttributes(fileName, attributes|windows.FILE_ATTRIBUTE_READONLY); err != nil {
		t.Fatal(err)
	}
	defer func() {
		current, err := windows.GetFileAttributes(fileName)
		if err == nil {
			_ = windows.SetFileAttributes(fileName, current&^windows.FILE_ATTRIBUTE_READONLY)
		}
	}()

	locked, err := lockAgentFileForMutation(filePath)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.close()
	originalReplace := replaceAgentFile
	replaceAgentFile = func(destination, replacement, backup *uint16) error {
		return simulateSuccessfulWindowsReplaceFile(
			windows.UTF16PtrToString(destination),
			windows.UTF16PtrToString(replacement),
			windows.UTF16PtrToString(backup),
		)
	}
	defer func() { replaceAgentFile = originalReplace }()

	if err := locked.replace([]byte("replacement")); err != nil {
		t.Fatalf("read-only source replacement failed metadata verification: %v", err)
	}
	publishedAttributes, err := windows.GetFileAttributes(fileName)
	if err != nil {
		t.Fatal(err)
	}
	if publishedAttributes&windows.FILE_ATTRIBUTE_READONLY == 0 {
		t.Fatalf("published attributes %#x lost source READONLY", publishedAttributes)
	}
	published, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(published) != "replacement" {
		t.Fatalf("published content=%q, want replacement", published)
	}
}

func TestReplacementByteRaceRetainsOriginalBackup(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(filePath, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	locked, err := lockAgentFileForMutation(filePath)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.close()

	originalReplace := replaceAgentFile
	defer func() { replaceAgentFile = originalReplace }()
	injected := []byte("malicious!!") // Same length as "replacement".
	replaceAgentFile = func(destination, replacement, backup *uint16) error {
		destinationPath := windows.UTF16PtrToString(destination)
		replacementPath := windows.UTF16PtrToString(replacement)
		backupPath := windows.UTF16PtrToString(backup)
		before, err := os.Stat(replacementPath)
		if err != nil {
			return err
		}
		if err := os.WriteFile(replacementPath, injected, before.Mode().Perm()); err != nil {
			return err
		}
		if err := os.Chtimes(replacementPath, before.ModTime(), before.ModTime()); err != nil {
			return err
		}
		return simulateSuccessfulWindowsReplaceFile(destinationPath, replacementPath, backupPath)
	}

	err = locked.replace([]byte("replacement"))
	if err == nil || !strings.Contains(err.Error(), "publication not verified") || !strings.Contains(err.Error(), "original retained") {
		t.Fatalf("replace error=%v, want unverified-publication and retained-original diagnostics", err)
	}
	published, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(published) != string(injected) {
		t.Fatalf("published race content=%q, want injected %q", published, injected)
	}
	backups, err := filepath.Glob(filepath.Join(dir, ".bv-backup-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 {
		t.Fatalf("retained backups=%v, want exactly one", backups)
	}
	original, err := os.ReadFile(backups[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(original) != "original" {
		t.Fatalf("retained original content=%q, want original", original)
	}
	replacements, err := filepath.Glob(filepath.Join(dir, ".bv-replace-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(replacements) != 0 {
		t.Fatalf("published replacement unexpectedly remains under its private name: %v", replacements)
	}
}

func TestReplacementSecurityRaceRetainsOriginalBackup(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(filePath, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	locked, err := lockAgentFileForMutation(filePath)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.close()

	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		t.Fatal(err)
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.GRANT_ACCESS,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_USER,
			TrusteeValue: windows.TrusteeValueFromSID(user.User.Sid),
		},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}

	originalReplace := replaceAgentFile
	defer func() { replaceAgentFile = originalReplace }()
	replaceAgentFile = func(destination, replacement, backup *uint16) error {
		destinationPath := windows.UTF16PtrToString(destination)
		replacementPath := windows.UTF16PtrToString(replacement)
		backupPath := windows.UTF16PtrToString(backup)
		if err := windows.SetNamedSecurityInfo(
			replacementPath,
			windows.SE_FILE_OBJECT,
			windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
			nil,
			nil,
			acl,
			nil,
		); err != nil {
			return err
		}
		return simulateSuccessfulWindowsReplaceFile(destinationPath, replacementPath, backupPath)
	}

	err = locked.replace([]byte("replacement"))
	if err == nil || !strings.Contains(err.Error(), "publication not verified") || !strings.Contains(err.Error(), "security metadata") || !strings.Contains(err.Error(), "original retained") {
		t.Fatalf("replace error=%v, want security-race refusal with retained-original diagnostic", err)
	}
	backups, err := filepath.Glob(filepath.Join(dir, ".bv-backup-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 {
		t.Fatalf("retained backups=%v, want exactly one", backups)
	}
	original, err := os.ReadFile(backups[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(original) != "original" {
		t.Fatalf("retained original content=%q, want original", original)
	}
}

func TestChangedOriginalMetadataBackupIsNeverRemovedAfterPublication(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(filePath, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	locked, err := lockAgentFileForMutation(filePath)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.close()
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		t.Fatal(err)
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.GRANT_ACCESS,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_USER,
			TrusteeValue: windows.TrusteeValueFromSID(user.User.Sid),
		},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}

	originalReplace := replaceAgentFile
	defer func() { replaceAgentFile = originalReplace }()
	replaceAgentFile = func(destination, replacement, backup *uint16) error {
		destinationPath := windows.UTF16PtrToString(destination)
		replacementPath := windows.UTF16PtrToString(replacement)
		backupPath := windows.UTF16PtrToString(backup)
		if err := windows.SetNamedSecurityInfo(
			destinationPath,
			windows.SE_FILE_OBJECT,
			windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
			nil,
			nil,
			acl,
			nil,
		); err != nil {
			return err
		}
		return simulateSuccessfulWindowsReplaceFile(destinationPath, replacementPath, backupPath)
	}

	err = locked.replace([]byte("replacement"))
	if err == nil || !strings.Contains(err.Error(), "original-backup cleanup or retention is ambiguous") {
		t.Fatalf("replace error=%v, want changed-original backup-retention diagnostic", err)
	}
	published, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(published) != "replacement" {
		t.Fatalf("published content=%q, want replacement", published)
	}
	backups, err := filepath.Glob(filepath.Join(dir, ".bv-backup-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 {
		t.Fatalf("retained backups=%v, want exactly one", backups)
	}
	changedOriginal, err := os.ReadFile(backups[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(changedOriginal) != "original" {
		t.Fatalf("retained changed-metadata original content=%q, want original", changedOriginal)
	}
}

func TestReplacementHardLinkRaceRetainsOriginalBackup(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(filePath, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	locked, err := lockAgentFileForMutation(filePath)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.close()

	originalReplace := replaceAgentFile
	defer func() { replaceAgentFile = originalReplace }()
	aliasPath := filepath.Join(dir, "replacement-peer-alias")
	replaceAgentFile = func(destination, replacement, backup *uint16) error {
		destinationPath := windows.UTF16PtrToString(destination)
		replacementPath := windows.UTF16PtrToString(replacement)
		backupPath := windows.UTF16PtrToString(backup)
		if err := os.Link(replacementPath, aliasPath); err != nil {
			return err
		}
		return simulateSuccessfulWindowsReplaceFile(destinationPath, replacementPath, backupPath)
	}

	err = locked.replace([]byte("replacement"))
	if err == nil || !strings.Contains(err.Error(), "publication not verified") || !strings.Contains(err.Error(), "hard links") || !strings.Contains(err.Error(), "original retained") {
		t.Fatalf("replace error=%v, want hard-link refusal with retained-original diagnostic", err)
	}
	published, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(published) != "replacement" {
		t.Fatalf("published content=%q, want replacement", published)
	}
	alias, err := os.ReadFile(aliasPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(alias) != "replacement" {
		t.Fatalf("hard-link alias content=%q, want replacement", alias)
	}
	backups, err := filepath.Glob(filepath.Join(dir, ".bv-backup-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 {
		t.Fatalf("retained backups=%v, want exactly one", backups)
	}
	original, err := os.ReadFile(backups[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(original) != "original" {
		t.Fatalf("retained original content=%q, want original", original)
	}
}

func TestReplacementAttributeRaceRetainsOriginalBackup(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(filePath, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	locked, err := lockAgentFileForMutation(filePath)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.close()

	originalReplace := replaceAgentFile
	defer func() { replaceAgentFile = originalReplace }()
	replaceAgentFile = func(destination, replacement, backup *uint16) error {
		destinationPath := windows.UTF16PtrToString(destination)
		replacementPath := windows.UTF16PtrToString(replacement)
		backupPath := windows.UTF16PtrToString(backup)
		if err := simulateSuccessfulWindowsReplaceFile(destinationPath, replacementPath, backupPath); err != nil {
			return err
		}
		destinationName, err := windowsAgentPathPointer(destinationPath)
		if err != nil {
			return err
		}
		attributes, err := windows.GetFileAttributes(destinationName)
		if err != nil {
			return err
		}
		return windows.SetFileAttributes(destinationName, attributes^windows.FILE_ATTRIBUTE_HIDDEN)
	}

	err = locked.replace([]byte("replacement"))
	if err == nil || !strings.Contains(err.Error(), "publication not verified") ||
		!strings.Contains(err.Error(), "attributes") || !strings.Contains(err.Error(), "original retained") {
		t.Fatalf("replace error=%v, want attribute-race refusal with retained-original diagnostic", err)
	}
	backups, err := filepath.Glob(filepath.Join(dir, ".bv-backup-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 {
		t.Fatalf("retained backups=%v, want exactly one", backups)
	}
	original, err := os.ReadFile(backups[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(original) != "original" {
		t.Fatalf("retained original content=%q, want original", original)
	}
}

func TestReplacementAlternateStreamRaceRetainsOriginalBackup(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(filePath, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	locked, err := lockAgentFileForMutation(filePath)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.close()

	originalReplace := replaceAgentFile
	defer func() { replaceAgentFile = originalReplace }()
	replaceAgentFile = func(destination, replacement, backup *uint16) error {
		destinationPath := windows.UTF16PtrToString(destination)
		replacementPath := windows.UTF16PtrToString(replacement)
		backupPath := windows.UTF16PtrToString(backup)
		preparedInfo, err := os.Stat(replacementPath)
		if err != nil {
			return err
		}
		if err := os.WriteFile(replacementPath+":bv-race", []byte("injected"), 0o600); err != nil {
			return err
		}
		// Isolate the stream-set mutation from the ordinary write-time
		// postcondition so this test fails specifically without stream metadata.
		if err := os.Chtimes(replacementPath, preparedInfo.ModTime(), preparedInfo.ModTime()); err != nil {
			return err
		}
		return simulateSuccessfulWindowsReplaceFile(destinationPath, replacementPath, backupPath)
	}

	err = locked.replace([]byte("replacement"))
	if err == nil || !strings.Contains(err.Error(), "publication not verified") ||
		!strings.Contains(err.Error(), "alternate data streams") || !strings.Contains(err.Error(), "original retained") {
		t.Fatalf("replace error=%v, want alternate-stream refusal with retained-original diagnostic", err)
	}
	backups, err := filepath.Glob(filepath.Join(dir, ".bv-backup-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 {
		t.Fatalf("retained backups=%v, want exactly one", backups)
	}
	original, err := os.ReadFile(backups[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(original) != "original" {
		t.Fatalf("retained original content=%q, want original", original)
	}
}

// simulateSuccessfulWindowsReplaceFile reproduces the source attributes and
// creation time that ReplaceFileW documents as preserved. Tests that inject a
// post-publication race use this helper instead of a bare pair of renames, whose
// metadata semantics are intentionally weaker than the production primitive.
func simulateSuccessfulWindowsReplaceFile(destinationPath, replacementPath, backupPath string) error {
	destinationName, err := windowsAgentPathPointer(destinationPath)
	if err != nil {
		return err
	}
	sourceHandle, err := windows.CreateFile(
		destinationName,
		windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		return err
	}
	var sourceInfo windows.ByHandleFileInformation
	infoErr := windows.GetFileInformationByHandle(sourceHandle, &sourceInfo)
	sourceCloseErr := windows.CloseHandle(sourceHandle)
	if err := errors.Join(infoErr, sourceCloseErr); err != nil {
		return err
	}

	replacementName, err := windowsAgentPathPointer(replacementPath)
	if err != nil {
		return err
	}
	replacementHandle, err := windows.CreateFile(
		replacementName,
		windows.FILE_WRITE_ATTRIBUTES,
		0,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		return err
	}
	timeErr := windows.SetFileTime(replacementHandle, &sourceInfo.CreationTime, nil, nil)
	replacementCloseErr := windows.CloseHandle(replacementHandle)
	if err := errors.Join(timeErr, replacementCloseErr); err != nil {
		return err
	}
	if err := windows.SetFileAttributes(replacementName, sourceInfo.FileAttributes); err != nil {
		return err
	}
	if err := os.Rename(destinationPath, backupPath); err != nil {
		return err
	}
	return os.Rename(replacementPath, destinationPath)
}

func TestReplaceFileFailureRetainsTempWhenOriginalIsProvenIntact(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(filePath, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	locked, err := lockAgentFileForMutation(filePath)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.close()

	originalReplace := replaceAgentFile
	defer func() { replaceAgentFile = originalReplace }()
	replaceAgentFile = func(_, _, _ *uint16) error {
		return windows.ERROR_ACCESS_DENIED
	}

	if err := locked.replace([]byte("replacement")); err == nil || !strings.Contains(err.Error(), "retained") {
		t.Fatalf("replace error=%v, want retained recovery-file diagnostic", err)
	}
	matches, err := filepath.Glob(filepath.Join(dir, ".bv-replace-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("retained replacements=%v, want exactly one", matches)
	}
	replacement, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(replacement) != "replacement" {
		t.Fatalf("retained replacement content=%q, want replacement", replacement)
	}
	content, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "original" {
		t.Fatalf("original changed after safe failure: %q", content)
	}
}
