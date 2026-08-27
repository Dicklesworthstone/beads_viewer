//go:build (darwin || linux) && !android

package agents

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

func TestAgentFileLockContentionIsBounded(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "AGENTS.md")
	if err := os.WriteFile(filePath, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	locked, err := lockAgentFileForMutation(filePath)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.close()

	started := time.Now()
	second, unlock, err := openAndLockAgentFileForMutation(filePath, 40*time.Millisecond)
	if second != nil {
		_ = second.Close()
	}
	if unlock != nil {
		unlock()
	}
	if !errors.Is(err, errAgentFileBusy) {
		t.Fatalf("contended lock error=%v, want busy", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("contended lock took %s, want a bounded failure", elapsed)
	}
}

func TestUnixAgentFileInspectionRejectsSymlinkWithoutFollowingIt(t *testing.T) {
	dir := t.TempDir()
	targetPath := filepath.Join(dir, "target.md")
	linkPath := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(targetPath, []byte(AppendBlurb("# Target\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(targetPath, linkPath); err != nil {
		t.Skipf("filesystem does not support symlink fixture: %v", err)
	}

	present, err := VerifyBlurbPresent(linkPath)
	if err == nil {
		t.Fatal("VerifyBlurbPresent() followed a symbolic-link final component")
	}
	if present {
		t.Fatal("VerifyBlurbPresent() reported content through a rejected symbolic link")
	}
}

func TestUnixAgentFileInspectionRejectsFIFOWithoutBlocking(t *testing.T) {
	fifoPath := filepath.Join(t.TempDir(), "AGENTS.md")
	if err := unix.Mkfifo(fifoPath, 0o600); err != nil {
		t.Skipf("filesystem does not support FIFO fixture: %v", err)
	}
	type result struct {
		present bool
		err     error
	}
	resultCh := make(chan result, 1)
	go func() {
		present, err := VerifyBlurbPresent(fifoPath)
		resultCh <- result{present: present, err: err}
	}()
	select {
	case got := <-resultCh:
		if got.err == nil || !strings.Contains(got.err.Error(), "non-regular") {
			t.Fatalf("VerifyBlurbPresent() error=%v, want non-regular FIFO refusal", got.err)
		}
		if got.present {
			t.Fatal("VerifyBlurbPresent() reported content from a FIFO")
		}
	case <-time.After(time.Second):
		t.Fatal("VerifyBlurbPresent() blocked while opening a FIFO")
	}
}

func TestCloseUnixAgentPrivateFileAfterFailureReportsRetainedPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".bv-replace-diagnostic")
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0)
	if err != nil {
		t.Fatal(err)
	}
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}

	err = closeUnixAgentPrivateFileAfterFailure(file, path, info, errors.New("injected preparation failure"))
	if err == nil || !strings.Contains(err.Error(), "injected preparation failure") ||
		!strings.Contains(err.Error(), "private replacement retained at") ||
		!strings.Contains(err.Error(), path) {
		t.Fatalf("failure diagnostic=%v, want cause and exact retained path", err)
	}
	retained, statErr := os.Lstat(path)
	if statErr != nil {
		t.Fatalf("retained candidate is not inspectable: %v", statErr)
	}
	if !retained.Mode().IsRegular() || !os.SameFile(info, retained) || retained.Mode().Perm() != 0 || retained.Size() != 0 {
		t.Fatalf("retained candidate changed: mode=%v size=%d", retained.Mode(), retained.Size())
	}
}

func TestCommitRejectsHardLinkedReplacementCandidate(t *testing.T) {
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
	replacement, replacementPath, replacementInfo, err := createAgentReplacementFile(locked)
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	aliasPath := filepath.Join(dir, "replacement-alias")
	if err := os.Link(replacementPath, aliasPath); err != nil {
		t.Skipf("filesystem does not support hard-link regression fixture: %v", err)
	}

	if _, err := commitAgentReplacement(locked, replacement, replacementPath, replacementInfo, nil); err == nil || !strings.Contains(err.Error(), "hard links") {
		t.Fatalf("commitAgentReplacement() error=%v, want candidate hard-link refusal", err)
	}
	content, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "original" {
		t.Fatalf("hard-linked candidate replaced source: %q", content)
	}
}

func TestUnixAtomicExchangePublishesAndTruncatesPrivateDisplacedOriginal(t *testing.T) {
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

	if err := locked.replace([]byte("replacement")); err != nil {
		t.Fatal(err)
	}
	published, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(published) != "replacement" {
		t.Fatalf("published content=%q, want replacement", published)
	}
	matches, err := filepath.Glob(filepath.Join(dir, ".bv-replace-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("displaced originals=%v, want exactly one retained exchange path", matches)
	}
	displacedInfo, err := os.Lstat(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	if !displacedInfo.Mode().IsRegular() || !os.SameFile(locked.info, displacedInfo) {
		t.Fatal("retained exchange path does not identify the locked original inode")
	}
	if displacedInfo.Mode().Perm() != 0 {
		t.Fatalf("retained displaced original mode=%o, want access-private mode 000", displacedInfo.Mode().Perm())
	}
	if displacedInfo.Size() != 0 {
		t.Fatalf("retained displaced original size=%d, want old bytes truncated to zero", displacedInfo.Size())
	}
}

func TestUnixAtomicExchangeTruncateFailureIsExplicitPartialPublication(t *testing.T) {
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

	originalTruncate := truncateUnixAgentDisplacedSource
	truncateUnixAgentDisplacedSource = func(*os.File) error {
		return errors.New("injected displaced-original truncate failure")
	}
	defer func() { truncateUnixAgentDisplacedSource = originalTruncate }()

	err = locked.replace([]byte("replacement"))
	if err == nil || !strings.Contains(err.Error(), "publication is partial") ||
		!strings.Contains(err.Error(), "injected displaced-original truncate failure") {
		t.Fatalf("replace error=%v, want explicit partial-publication truncate failure", err)
	}
	published, readErr := os.ReadFile(filePath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(published) != "replacement" {
		t.Fatalf("published content=%q, want replacement", published)
	}
	matches, globErr := filepath.Glob(filepath.Join(dir, ".bv-replace-*"))
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(matches) != 1 || !strings.Contains(err.Error(), matches[0]) {
		t.Fatalf("partial-publication error=%v, want exact retained path from %v", err, matches)
	}
	info, statErr := os.Lstat(matches[0])
	if statErr != nil {
		t.Fatal(statErr)
	}
	if info.Mode().Perm() != 0 || info.Size() != int64(len("original")) {
		t.Fatalf("retained original mode=%o size=%d, want mode 000 and untruncated bytes", info.Mode().Perm(), info.Size())
	}
}

func TestUnixAtomicExchangeDisplacedSyncFailureRetainsVerifiedRecovery(t *testing.T) {
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

	originalExchange := exchangeUnixAgentFilePaths
	originalSync := syncUnixAgentDisplacedSource
	var displacedPath string
	exchangeUnixAgentFilePaths = func(sourcePath, destinationPath string) error {
		displacedPath = sourcePath
		return originalExchange(sourcePath, destinationPath)
	}
	syncUnixAgentDisplacedSource = func(*os.File) error {
		return errors.New("injected truncated-displaced sync failure")
	}
	defer func() {
		exchangeUnixAgentFilePaths = originalExchange
		syncUnixAgentDisplacedSource = originalSync
	}()

	err = locked.replace([]byte("replacement"))
	if err == nil || !strings.Contains(err.Error(), "publication is partial") ||
		!strings.Contains(err.Error(), "injected truncated-displaced sync failure") ||
		!strings.Contains(err.Error(), displacedPath) {
		t.Fatalf("replace error=%v, want explicit sync failure naming displaced path %q", err, displacedPath)
	}
	published, readErr := os.ReadFile(filePath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(published) != "replacement" {
		t.Fatalf("published content=%q, want replacement", published)
	}
	displacedInfo, statErr := os.Lstat(displacedPath)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if !os.SameFile(locked.info, displacedInfo) || displacedInfo.Mode().Perm() != 0 || displacedInfo.Size() != 0 {
		t.Fatalf("displaced original after failed sync: mode=%v size=%d, want locked inode mode 000 size 0", displacedInfo.Mode(), displacedInfo.Size())
	}
	matches, globErr := filepath.Glob(filepath.Join(dir, ".bv-replace-*"))
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(matches) != 2 {
		t.Fatalf("displaced/recovery paths=%v, want truncated inode plus exact-byte recovery", matches)
	}
	for _, path := range matches {
		if path == displacedPath {
			continue
		}
		if !strings.Contains(err.Error(), path) {
			t.Fatalf("replace error=%v does not name recovery path %q", err, path)
		}
		if chmodErr := os.Chmod(path, 0o400); chmodErr != nil {
			t.Fatal(chmodErr)
		}
		recovered, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if string(recovered) != "original" {
			t.Fatalf("recovered original content=%q, want original", recovered)
		}
	}
}

func TestPublishedReplacementCloseFailureReportsPartialPublicationOnce(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "AGENTS.md")
	if err := os.WriteFile(filePath, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	locked, err := lockAgentFileForMutation(filePath)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.close()

	originalClose := closeAgentReplacementFile
	closeCalls := 0
	closeAgentReplacementFile = func(file *os.File) error {
		closeCalls++
		return errors.Join(file.Close(), errors.New("injected published-handle close failure"))
	}
	defer func() { closeAgentReplacementFile = originalClose }()

	err = locked.replace([]byte("replacement"))
	if err == nil || !strings.Contains(err.Error(), "publication is partial") ||
		!strings.Contains(err.Error(), "injected published-handle close failure") {
		t.Fatalf("replace error=%v, want explicit post-publication close failure", err)
	}
	if closeCalls != 1 {
		t.Fatalf("replacement close calls=%d, want exactly one", closeCalls)
	}
	published, readErr := os.ReadFile(filePath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(published) != "replacement" {
		t.Fatalf("published content=%q, want replacement", published)
	}
}

func TestPublishedReplacementInspectionCloseFailureIsPropagatedOnce(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "AGENTS.md")
	if err := os.WriteFile(filePath, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	locked, err := lockAgentFileForMutation(filePath)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.close()

	originalClose := closeAgentInspectionFile
	closeCalls := 0
	closeAgentInspectionFile = func(file *os.File) error {
		closeCalls++
		return errors.Join(file.Close(), errors.New("injected published-inspection close failure"))
	}
	defer func() { closeAgentInspectionFile = originalClose }()

	err = locked.replace([]byte("replacement"))
	if err == nil || !strings.Contains(err.Error(), "publication is partial") ||
		!strings.Contains(err.Error(), "injected published-inspection close failure") {
		t.Fatalf("replace error=%v, want explicit published-inspection close failure", err)
	}
	if closeCalls != 1 {
		t.Fatalf("published inspection close calls=%d, want exactly one", closeCalls)
	}
	published, readErr := os.ReadFile(filePath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(published) != "replacement" {
		t.Fatalf("published content=%q, want replacement", published)
	}
}

func TestVerifyBlurbPresentPropagatesInspectionCloseFailureOnce(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "AGENTS.md")
	if err := os.WriteFile(filePath, []byte(AppendBlurb("# Instructions\n")), 0o600); err != nil {
		t.Fatal(err)
	}

	originalClose := closeAgentInspectionFile
	closeCalls := 0
	closeAgentInspectionFile = func(file *os.File) error {
		closeCalls++
		return errors.Join(file.Close(), errors.New("injected blurb-inspection close failure"))
	}
	defer func() { closeAgentInspectionFile = originalClose }()

	present, err := VerifyBlurbPresent(filePath)
	if err == nil || !strings.Contains(err.Error(), "injected blurb-inspection close failure") {
		t.Fatalf("VerifyBlurbPresent() error=%v, want inspection-close failure", err)
	}
	if present {
		t.Fatal("VerifyBlurbPresent() reported success despite inspection-close failure")
	}
	if closeCalls != 1 {
		t.Fatalf("blurb inspection close calls=%d, want exactly one", closeCalls)
	}
}

func TestExclusiveCreatePropagatesCloseFailureExactlyOnce(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "AGENTS.md")
	originalClose := closeAgentCreateFile
	closeCalls := 0
	closeAgentCreateFile = func(file *os.File) error {
		closeCalls++
		return errors.Join(file.Close(), errors.New("injected exclusive-create close failure"))
	}
	defer func() { closeAgentCreateFile = originalClose }()

	err := CreateAgentFile(filePath)
	if err == nil || !strings.Contains(err.Error(), "injected exclusive-create close failure") ||
		!strings.Contains(err.Error(), "unpublished file retained at") {
		t.Fatalf("CreateAgentFile() error=%v, want close failure and retained-path diagnostic", err)
	}
	if closeCalls != 1 {
		t.Fatalf("exclusive-create close calls=%d, want exactly one", closeCalls)
	}
	if _, statErr := os.Lstat(filePath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("destination stat error=%v, want unpublished destination", statErr)
	}
	matches, globErr := filepath.Glob(filepath.Join(dir, ".bv-create-*"))
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(matches) != 1 || !strings.Contains(err.Error(), matches[0]) {
		t.Fatalf("retained create candidates=%v error=%v, want exact retained path", matches, err)
	}
}

func TestExclusiveCreateInspectionCloseFailureReportsPartialPublication(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "AGENTS.md")
	originalClose := closeAgentInspectionFile
	closeCalls := 0
	closeAgentInspectionFile = func(file *os.File) error {
		closeCalls++
		return errors.Join(file.Close(), errors.New("injected published-create inspection close failure"))
	}
	defer func() { closeAgentInspectionFile = originalClose }()

	err := CreateAgentFile(filePath)
	if err == nil || !strings.Contains(err.Error(), "destination was published at \""+filePath+"\"") ||
		!strings.Contains(err.Error(), "publication is partial") ||
		!strings.Contains(err.Error(), "injected published-create inspection close failure") {
		t.Fatalf("CreateAgentFile() error=%v, want explicit post-publication inspection-close failure", err)
	}
	if closeCalls != 1 {
		t.Fatalf("published-create inspection close calls=%d, want exactly one", closeCalls)
	}
	published, readErr := os.ReadFile(filePath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	want := "# AI Agent Instructions\n\n" + AgentBlurb + "\n"
	if string(published) != want {
		t.Fatalf("published content=%q, want complete agent file", published)
	}
}

func TestUnixAtomicExchangeUnsupportedFailsBeforePublication(t *testing.T) {
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

	originalExchange := exchangeUnixAgentFilePaths
	exchangeUnixAgentFilePaths = func(string, string) error {
		return errors.New("injected unsupported atomic exchange")
	}
	defer func() { exchangeUnixAgentFilePaths = originalExchange }()

	err = locked.replace([]byte("replacement"))
	if err == nil || !strings.Contains(err.Error(), "exchange replacement with destination") ||
		!strings.Contains(err.Error(), "injected unsupported atomic exchange") {
		t.Fatalf("replace error=%v, want unsupported-exchange refusal", err)
	}
	original, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(original) != "original" {
		t.Fatalf("source content=%q after unsupported exchange, want original", original)
	}
	matches, err := filepath.Glob(filepath.Join(dir, ".bv-replace-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("retained unpublished candidates=%v, want exactly one", matches)
	}
	info, err := os.Lstat(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0 {
		t.Fatalf("retained unpublished candidate mode=%v, want regular mode 000", info.Mode())
	}
}

func TestUnixAtomicExchangePrivacyFailureReportsExactRetainedPath(t *testing.T) {
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

	originalMakePrivate := makeUnixAgentDisplacedSourcePrivate
	makeUnixAgentDisplacedSourcePrivate = func(*os.File) error {
		return errors.New("injected displaced-source privacy failure")
	}
	defer func() { makeUnixAgentDisplacedSourcePrivate = originalMakePrivate }()

	err = locked.replace([]byte("replacement"))
	if err == nil || !strings.Contains(err.Error(), "replacement was published") ||
		!strings.Contains(err.Error(), "could not be made access-private") ||
		!strings.Contains(err.Error(), "injected displaced-source privacy failure") {
		t.Fatalf("replace error=%v, want explicit partial-publication privacy diagnostic", err)
	}
	published, readErr := os.ReadFile(filePath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(published) != "replacement" {
		t.Fatalf("published content=%q, want replacement", published)
	}
	matches, err := filepath.Glob(filepath.Join(dir, ".bv-replace-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("retained displaced originals=%v, want exactly one", matches)
	}
	if !strings.Contains(err.Error(), matches[0]) {
		t.Fatalf("partial-publication error=%v does not name retained path %q", err, matches[0])
	}
	retained, readErr := os.ReadFile(matches[0])
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(retained) != "original" {
		t.Fatalf("retained original content=%q, want original", retained)
	}
}

func TestUnixAtomicExchangeDetectsPeerReplacementInFinalGap(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "AGENTS.md")
	peerPath := filepath.Join(dir, "peer.md")
	if err := os.WriteFile(filePath, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(peerPath, []byte("peer"), 0o600); err != nil {
		t.Fatal(err)
	}
	locked, err := lockAgentFileForMutation(filePath)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.close()

	originalExchange := exchangeUnixAgentFilePaths
	var displacedPath string
	exchangeUnixAgentFilePaths = func(sourcePath, destinationPath string) error {
		displacedPath = sourcePath
		if err := os.Rename(peerPath, destinationPath); err != nil {
			return err
		}
		return originalExchange(sourcePath, destinationPath)
	}
	defer func() { exchangeUnixAgentFilePaths = originalExchange }()

	err = locked.replace([]byte("replacement"))
	if err == nil || !strings.Contains(err.Error(), "atomic exchange displaced an unexpected destination") ||
		!strings.Contains(err.Error(), "unexpected displaced data retained at") ||
		!strings.Contains(err.Error(), "original bytes retained at") ||
		!strings.Contains(err.Error(), displacedPath) {
		t.Fatalf("replace error=%v, want peer-displacement and two-path recovery diagnostic", err)
	}
	published, readErr := os.ReadFile(filePath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(published) != "replacement" {
		t.Fatalf("published content=%q, want replacement", published)
	}
	displacedPeer, readErr := os.ReadFile(displacedPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(displacedPeer) != "peer" {
		t.Fatalf("displaced peer content=%q, want peer", displacedPeer)
	}
	matches, err := filepath.Glob(filepath.Join(dir, ".bv-replace-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 {
		t.Fatalf("exchange/recovery paths=%v, want displaced peer and original recovery", matches)
	}
	for _, path := range matches {
		if path == displacedPath {
			continue
		}
		if !strings.Contains(err.Error(), path) {
			t.Fatalf("replace error=%v does not name original recovery path %q", err, path)
		}
		if chmodErr := os.Chmod(path, 0o400); chmodErr != nil {
			t.Fatalf("make original recovery readable: %v", chmodErr)
		}
		recovered, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if string(recovered) != "original" {
			t.Fatalf("recovered original content=%q, want original", recovered)
		}
	}
}

func TestUnixAtomicExchangeDetectsInPlaceSourceMutationInFinalGap(t *testing.T) {
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

	originalExchange := exchangeUnixAgentFilePaths
	var displacedPath string
	exchangeUnixAgentFilePaths = func(sourcePath, destinationPath string) error {
		displacedPath = sourcePath
		if err := os.WriteFile(destinationPath, []byte("tampered"), 0o600); err != nil {
			return err
		}
		if err := os.Chtimes(destinationPath, locked.info.ModTime(), locked.info.ModTime()); err != nil {
			return err
		}
		return originalExchange(sourcePath, destinationPath)
	}
	defer func() { exchangeUnixAgentFilePaths = originalExchange }()

	err = locked.replace([]byte("replacement"))
	if err == nil || !strings.Contains(err.Error(), "atomic exchange displaced an unexpected destination") ||
		!strings.Contains(err.Error(), "bytes differ") ||
		!strings.Contains(err.Error(), "original bytes retained at") ||
		!strings.Contains(err.Error(), displacedPath) {
		t.Fatalf("replace error=%v, want in-place-mutation and recovery diagnostic", err)
	}
	published, readErr := os.ReadFile(filePath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(published) != "replacement" {
		t.Fatalf("published content=%q, want replacement", published)
	}
	displacedMutation, readErr := os.ReadFile(displacedPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(displacedMutation) != "tampered" {
		t.Fatalf("displaced mutated content=%q, want tampered", displacedMutation)
	}
}

func TestUnixAtomicExchangeDetectsSourceHardLinkInFinalGap(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "AGENTS.md")
	aliasPath := filepath.Join(dir, "source-peer-alias")
	if err := os.WriteFile(filePath, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	locked, err := lockAgentFileForMutation(filePath)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.close()

	originalExchange := exchangeUnixAgentFilePaths
	var displacedPath string
	exchangeUnixAgentFilePaths = func(sourcePath, destinationPath string) error {
		displacedPath = sourcePath
		if err := os.Link(destinationPath, aliasPath); err != nil {
			return err
		}
		return originalExchange(sourcePath, destinationPath)
	}
	defer func() { exchangeUnixAgentFilePaths = originalExchange }()

	err = locked.replace([]byte("replacement"))
	if errors.Is(err, os.ErrPermission) || errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EOPNOTSUPP) {
		t.Skipf("filesystem does not support hard-link race fixture: %v", err)
	}
	if err == nil || !strings.Contains(err.Error(), "atomic exchange displaced an unexpected destination") ||
		!strings.Contains(err.Error(), "hard links") ||
		!strings.Contains(err.Error(), "original bytes retained at") ||
		!strings.Contains(err.Error(), displacedPath) {
		t.Fatalf("replace error=%v, want hard-link and recovery diagnostic", err)
	}
	published, readErr := os.ReadFile(filePath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(published) != "replacement" {
		t.Fatalf("published content=%q, want replacement", published)
	}
	for _, path := range []string{displacedPath, aliasPath} {
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if string(content) != "original" {
			t.Fatalf("retained original at %q has content %q", path, content)
		}
	}
}

func TestUnixCommitRejectsFinalizedMetadataRace(t *testing.T) {
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

	originalVerify := verifyFinalizedUnixAgentReplacement
	verifyFinalizedUnixAgentReplacement = func(source, replacement *os.File) error {
		if err := replacement.Chmod(0o666); err != nil {
			return err
		}
		return originalVerify(source, replacement)
	}
	defer func() { verifyFinalizedUnixAgentReplacement = originalVerify }()

	err = locked.replace([]byte("replacement"))
	if err == nil || !strings.Contains(err.Error(), "verify finalized replacement metadata") || !strings.Contains(err.Error(), "mode") {
		t.Fatalf("replace error=%v, want finalized-mode race refusal", err)
	}
	original, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(original) != "original" {
		t.Fatalf("source changed despite finalized-metadata refusal: %q", original)
	}
	matches, err := filepath.Glob(filepath.Join(dir, ".bv-replace-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("retained replacements=%v, want one refused candidate", matches)
	}
	info, err := os.Stat(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0 {
		t.Fatalf("refused candidate mode=%o, want resecured mode 000", info.Mode().Perm())
	}
}

func TestUnixPostPublicationVerificationFailureRetainsOriginalBytes(t *testing.T) {
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

	originalVerify := verifyPublishedUnixAgentReplacement
	verifyPublishedUnixAgentReplacement = func(string, os.FileInfo, []byte) error {
		return errors.New("injected post-publication verification failure")
	}
	defer func() { verifyPublishedUnixAgentReplacement = originalVerify }()

	err = locked.replace([]byte("replacement"))
	if err == nil || !strings.Contains(err.Error(), "verify published replacement") || !strings.Contains(err.Error(), "original bytes retained at") {
		t.Fatalf("replace error=%v, want verification failure with retained-original diagnostic", err)
	}
	assertUnixPublishedAndOriginalRecovery(t, filePath, "replacement", "original")
}

func TestUnixPostPublicationPrivatePathSwapMaterializesVerifiedRecovery(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "AGENTS.md")
	peerPath := filepath.Join(dir, "peer-private-swap")
	if err := os.WriteFile(filePath, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(peerPath, []byte("peer"), 0o600); err != nil {
		t.Fatal(err)
	}
	locked, err := lockAgentFileForMutation(filePath)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.close()

	originalVerify := verifyPublishedUnixAgentReplacement
	originalExchange := exchangeUnixAgentFilePaths
	originalMakePrivate := makeUnixAgentDisplacedSourcePrivate
	var displacedPath string
	verifyPublishedUnixAgentReplacement = func(string, os.FileInfo, []byte) error {
		return errors.New("injected post-publication verification failure")
	}
	exchangeUnixAgentFilePaths = func(sourcePath, destinationPath string) error {
		displacedPath = sourcePath
		return originalExchange(sourcePath, destinationPath)
	}
	makeUnixAgentDisplacedSourcePrivate = func(file *os.File) error {
		if err := originalMakePrivate(file); err != nil {
			return err
		}
		return originalExchange(peerPath, displacedPath)
	}
	defer func() {
		verifyPublishedUnixAgentReplacement = originalVerify
		exchangeUnixAgentFilePaths = originalExchange
		makeUnixAgentDisplacedSourcePrivate = originalMakePrivate
	}()

	err = locked.replace([]byte("replacement"))
	if err == nil || !strings.Contains(err.Error(), "failed private revalidation") ||
		!strings.Contains(err.Error(), displacedPath) ||
		!strings.Contains(err.Error(), "original bytes retained at") {
		t.Fatalf("replace error=%v, want failed-path and verified-recovery diagnostic", err)
	}
	published, readErr := os.ReadFile(filePath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(published) != "replacement" {
		t.Fatalf("published content=%q, want replacement", published)
	}
	displacedPeer, readErr := os.ReadFile(displacedPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(displacedPeer) != "peer" {
		t.Fatalf("failed displaced path content=%q, want peer", displacedPeer)
	}
	matches, globErr := filepath.Glob(filepath.Join(dir, ".bv-replace-*"))
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(matches) != 2 {
		t.Fatalf("exchange/recovery paths=%v, want failed displaced path and verified recovery", matches)
	}
	for _, path := range matches {
		if path == displacedPath {
			continue
		}
		if !strings.Contains(err.Error(), path) {
			t.Fatalf("replace error=%v does not name verified recovery path %q", err, path)
		}
		if chmodErr := os.Chmod(path, 0o400); chmodErr != nil {
			t.Fatal(chmodErr)
		}
		recovered, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if string(recovered) != "original" {
			t.Fatalf("recovered content=%q, want original", recovered)
		}
	}
}

func TestUnixPostPublicationHardLinkRaceRetainsOriginalBytes(t *testing.T) {
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

	originalVerify := verifyPublishedUnixAgentReplacement
	aliasPath := filepath.Join(dir, "published-peer-alias")
	verifyPublishedUnixAgentReplacement = func(path string, info os.FileInfo, content []byte) error {
		if err := os.Link(path, aliasPath); err != nil {
			t.Skipf("filesystem cannot create hard-link race fixture: %v", err)
		}
		return originalVerify(path, info, content)
	}
	defer func() { verifyPublishedUnixAgentReplacement = originalVerify }()

	err = locked.replace([]byte("replacement"))
	if err == nil || !strings.Contains(err.Error(), "hard links") || !strings.Contains(err.Error(), "original bytes retained at") {
		t.Fatalf("replace error=%v, want hard-link refusal with retained-original diagnostic", err)
	}
	assertUnixPublishedAndOriginalRecovery(t, filePath, "replacement", "original")
	alias, err := os.ReadFile(aliasPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(alias) != "replacement" {
		t.Fatalf("published hard-link alias content=%q, want replacement", alias)
	}
}

func TestUnixPostPublicationMetadataRaceRetainsOriginalBytes(t *testing.T) {
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

	originalVerify := verifyPublishedUnixAgentMetadata
	verifyPublishedUnixAgentMetadata = func(source, replacement *os.File) error {
		// Mutate only after the pathname/byte verifier has completed. This shapes
		// the exact final-rename-gap race that a stability-only postcheck misses.
		if err := replacement.Chmod(0o666); err != nil {
			return err
		}
		return originalVerify(source, replacement)
	}
	defer func() { verifyPublishedUnixAgentMetadata = originalVerify }()

	err = locked.replace([]byte("replacement"))
	if err == nil || !strings.Contains(err.Error(), "verify published replacement metadata") ||
		!strings.Contains(err.Error(), "mode") || !strings.Contains(err.Error(), "original bytes retained at") {
		t.Fatalf("replace error=%v, want post-publication metadata refusal with retained-original diagnostic", err)
	}
	assertUnixPublishedAndOriginalRecovery(t, filePath, "replacement", "original")
}

func TestUnixPostPrivacyPublishedXattrMutationIsRejected(t *testing.T) {
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

	originalMakePrivate := makeUnixAgentDisplacedSourcePrivate
	makeUnixAgentDisplacedSourcePrivate = func(file *os.File) error {
		if err := originalMakePrivate(file); err != nil {
			return err
		}
		return unix.Setxattr(filePath, "user.beadsviewer.postprivacy", []byte("injected"), 0)
	}
	defer func() { makeUnixAgentDisplacedSourcePrivate = originalMakePrivate }()

	err = locked.replace([]byte("replacement"))
	if errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EOPNOTSUPP) {
		t.Skipf("filesystem does not support the post-privacy xattr race fixture: %v", err)
	}
	if err == nil || !strings.Contains(err.Error(), "published replacement metadata changed") ||
		!strings.Contains(err.Error(), "original bytes retained at") {
		t.Fatalf("replace error=%v, want post-privacy finalized-metadata refusal", err)
	}
	published, readErr := os.ReadFile(filePath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(published) != "replacement" {
		t.Fatalf("published content=%q, want replacement", published)
	}
	assertUnixPublishedAndOriginalRecovery(t, filePath, "replacement", "original")
}

func TestUnixRecoverySkipsReplacementOnlyPreflightAfterPublication(t *testing.T) {
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

	originalVerify := verifyPublishedUnixAgentMetadata
	originalPreflight := preflightAgentSourceExtendedAttributes
	verifyPublishedUnixAgentMetadata = func(*os.File, *os.File) error {
		// Shape a final-gap metadata race: publication has happened, its metadata
		// check refuses, and the source now fails policy intended only for creating
		// a publishable replacement. Recovery must still retain the locked bytes.
		preflightAgentSourceExtendedAttributes = func(*os.File) error {
			return errors.New("injected replacement-only source metadata refusal")
		}
		return errors.New("injected post-publication metadata failure")
	}
	defer func() {
		verifyPublishedUnixAgentMetadata = originalVerify
		preflightAgentSourceExtendedAttributes = originalPreflight
	}()

	err = locked.replace([]byte("replacement"))
	if err == nil || !strings.Contains(err.Error(), "verify published replacement metadata") ||
		!strings.Contains(err.Error(), "original bytes retained at") ||
		strings.Contains(err.Error(), "injected replacement-only source metadata refusal") {
		t.Fatalf("replace error=%v, want metadata refusal with recovery independent of replacement preflight", err)
	}
	assertUnixPublishedAndOriginalRecovery(t, filePath, "replacement", "original")
}

func TestUnixPostPublicationDirectorySyncFailureRetainsOriginalBytes(t *testing.T) {
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

	originalSync := syncPublishedUnixAgentDirectory
	syncPublishedUnixAgentDirectory = func(string) error {
		return errors.New("injected post-publication directory-sync failure")
	}
	defer func() { syncPublishedUnixAgentDirectory = originalSync }()

	err = locked.replace([]byte("replacement"))
	if err == nil || !strings.Contains(err.Error(), "sync replacement directory") || !strings.Contains(err.Error(), "original bytes retained at") {
		t.Fatalf("replace error=%v, want directory-sync failure with retained-original diagnostic", err)
	}
	assertUnixPublishedAndOriginalRecovery(t, filePath, "replacement", "original")
}

func TestUnixRecoveryReportsAllocatedPathAfterCloseOrPostCloseStatFailure(t *testing.T) {
	for _, tt := range []struct {
		name   string
		inject func(t *testing.T)
		want   string
	}{
		{
			name: "close failure",
			inject: func(t *testing.T) {
				originalClose := closeUnixAgentRecoveryFile
				closeUnixAgentRecoveryFile = func(file *os.File) error {
					return errors.Join(file.Close(), errors.New("injected recovery close failure"))
				}
				t.Cleanup(func() { closeUnixAgentRecoveryFile = originalClose })
			},
			want: "close original recovery file",
		},
		{
			name: "post-close stat failure",
			inject: func(t *testing.T) {
				originalLstat := lstatUnixAgentRecoveryPath
				calls := 0
				lstatUnixAgentRecoveryPath = func(path string) (os.FileInfo, error) {
					calls++
					if calls == 2 {
						return nil, errors.New("injected post-close stat failure")
					}
					return os.Lstat(path)
				}
				t.Cleanup(func() { lstatUnixAgentRecoveryPath = originalLstat })
			},
			want: "stat closed original recovery path",
		},
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
			tt.inject(t)

			recoveryErr := unixAgentPostPublicationFailure(locked, errors.New("injected publication failure"))
			if recoveryErr == nil || !strings.Contains(recoveryErr.Error(), tt.want) ||
				!strings.Contains(recoveryErr.Error(), "original recovery path allocated at") ||
				strings.Contains(recoveryErr.Error(), "failed to retain original recovery bytes") {
				t.Fatalf("recovery error=%v, want %q", recoveryErr, tt.want)
			}
			matches, err := filepath.Glob(filepath.Join(dir, ".bv-replace-*"))
			if err != nil {
				t.Fatal(err)
			}
			if len(matches) != 1 {
				t.Fatalf("allocated recovery paths=%v, want exactly one", matches)
			}
			recoveryPath := matches[0]
			if !strings.Contains(recoveryErr.Error(), recoveryPath) {
				t.Fatalf("recovery error=%v does not identify allocated path %q", recoveryErr, recoveryPath)
			}
			info, err := os.Lstat(recoveryPath)
			if err != nil {
				t.Fatalf("allocated recovery path is not inspectable: %v", err)
			}
			if !info.Mode().IsRegular() || info.Mode().Perm() != 0 {
				t.Fatalf("allocated recovery mode=%v, want regular mode 000", info.Mode())
			}
			if err := os.Chmod(recoveryPath, 0o400); err != nil {
				t.Fatalf("make allocated recovery readable for assertion: %v", err)
			}
			content, err := os.ReadFile(recoveryPath)
			if err != nil {
				t.Fatal(err)
			}
			if string(content) != "original" {
				t.Fatalf("allocated recovery content=%q, want original", content)
			}
		})
	}
}

func assertUnixPublishedAndOriginalRecovery(t *testing.T, filePath, wantPublished, wantOriginal string) {
	t.Helper()
	published, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(published) != wantPublished {
		t.Fatalf("published content=%q, want %q", published, wantPublished)
	}
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(filePath), ".bv-replace-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("original recovery files=%v, want exactly one", matches)
	}
	info, err := os.Stat(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0 {
		t.Fatalf("original recovery mode=%v, want regular mode 000", info.Mode())
	}
	if err := os.Chmod(matches[0], 0o400); err != nil {
		t.Fatalf("make original recovery readable for assertion: %v", err)
	}
	recovered, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(recovered) != wantOriginal {
		t.Fatalf("original recovery content=%q, want %q", recovered, wantOriginal)
	}
}

func TestDarwinPostPublicationFailureUsesExchangedOriginalDespiteNewDirectoryACL(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Darwin recovery ACL regression")
	}
	dir := t.TempDir()
	filePath := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(filePath, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if before := darwinACLListing(t, filePath); before != "" {
		t.Fatalf("source unexpectedly started with an ACL: %q", before)
	}
	userOutput, err := exec.Command("id", "-un").Output()
	if err != nil {
		t.Fatal(err)
	}
	inheritableACL := strings.TrimSpace(string(userOutput)) + " allow read,file_inherit"

	locked, err := lockAgentFileForMutation(filePath)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.close()
	originalVerify := verifyPublishedUnixAgentReplacement
	var aclInstallErr error
	verifyPublishedUnixAgentReplacement = func(path string, info os.FileInfo, content []byte) error {
		if err := originalVerify(path, info, content); err != nil {
			return err
		}
		if output, err := exec.Command("chmod", "+a", inheritableACL, dir).CombinedOutput(); err != nil {
			aclInstallErr = errors.New(string(output))
			return aclInstallErr
		}
		return errors.New("injected post-publication verification failure after ACL inheritance changed")
	}
	defer func() { verifyPublishedUnixAgentReplacement = originalVerify }()
	defer func() { _, _ = exec.Command("chmod", "-N", dir).CombinedOutput() }()

	err = locked.replace([]byte("replacement"))
	if aclInstallErr != nil {
		t.Skipf("cannot create a Darwin inheritable ACL fixture: %v", aclInstallErr)
	}
	if err == nil || !strings.Contains(err.Error(), "injected post-publication verification failure") ||
		!strings.Contains(err.Error(), "original bytes retained at") {
		t.Fatalf("replace error=%v, want direct exchanged-original recovery diagnostic", err)
	}
	published, readErr := os.ReadFile(filePath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(published) != "replacement" {
		t.Fatalf("published content=%q, want replacement", published)
	}
	// The exchanged original existed before the parent ACL changed, so recovery
	// does not allocate a new inode that could inherit the broader policy.
	assertUnixPublishedAndOriginalRecovery(t, filePath, "replacement", "original")
}

func TestWriteReplacementPreservesExtendedAttributes(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "AGENTS.md")
	if err := os.WriteFile(filePath, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	const attribute = "user.beadsviewer.test"
	want := []byte("preserve-me")
	if err := unix.Setxattr(filePath, attribute, want, 0); err != nil {
		if errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EPERM) {
			t.Skipf("filesystem does not support a writable test xattr: %v", err)
		}
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

	size, err := unix.Getxattr(filePath, attribute, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]byte, size)
	if _, err := unix.Getxattr(filePath, attribute, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("extended attribute=%q, want %q", got, want)
	}
}

func TestCopyAgentExtendedAttributesRemovesReplacementOnlyAttributes(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.md")
	replacementPath := filepath.Join(dir, "replacement.md")
	if err := os.WriteFile(sourcePath, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(replacementPath, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	const sourceAttribute = "user.beadsviewer.source"
	const replacementOnlyAttribute = "user.beadsviewer.inherited"
	if err := unix.Setxattr(sourcePath, sourceAttribute, []byte("source-value"), 0); err != nil {
		if errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EPERM) {
			t.Skipf("filesystem does not support writable test xattrs: %v", err)
		}
		t.Fatal(err)
	}
	if err := unix.Setxattr(replacementPath, replacementOnlyAttribute, []byte("remove-me"), 0); err != nil {
		t.Fatal(err)
	}

	source, err := os.Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	replacement, err := os.OpenFile(replacementPath, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	if err := copyAgentExtendedAttributes(source, replacement); err != nil {
		t.Fatal(err)
	}
	if _, err := unix.Getxattr(replacementPath, replacementOnlyAttribute, nil); err == nil {
		t.Fatalf("replacement-only extended attribute %q survived exact copy", replacementOnlyAttribute)
	}
	size, err := unix.Getxattr(replacementPath, sourceAttribute, nil)
	if err != nil {
		t.Fatal(err)
	}
	value := make([]byte, size)
	if _, err := unix.Getxattr(replacementPath, sourceAttribute, value); err != nil {
		t.Fatal(err)
	}
	if string(value) != "source-value" {
		t.Fatalf("copied source extended attribute=%q, want source-value", value)
	}
}

func TestContentBoundIntegrityAttributesRequireRecomputation(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{name: "security.ima", want: true},
		{name: "security.evm", want: true},
		{name: "security.capability", want: true},
		{name: "com.apple.cs.CodeDirectory", want: true},
		{name: "com.apple.cs.CodeRequirements", want: true},
		{name: "com.apple.cs.CodeRequirements-1", want: true},
		{name: "com.apple.cs.CodeSignature", want: true},
		{name: "com.apple.cs.CodeEntitlements", want: true},
		{name: "com.apple.cs.FutureFormat", want: true},
		{name: "user.beadsviewer.test", want: false},
		{name: "security.selinux", want: false},
		{name: "com.apple.quarantine", want: false},
		{name: "com.apple.cs", want: false},
		{name: "com.apple.csx.CodeSignature", want: false},
		{name: "Com.apple.cs.CodeSignature", want: false},
	}
	for _, tt := range tests {
		if got := agentExtendedAttributeRequiresRecomputation(tt.name); got != tt.want {
			t.Errorf("agentExtendedAttributeRequiresRecomputation(%q)=%t, want %t", tt.name, got, tt.want)
		}
	}
}

func TestContentBoundXattrPreflightRejectsBeforeReplacementBytesExist(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(filePath, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	locked, err := lockAgentFileForMutation(filePath)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.close()

	originalPreflight := preflightAgentSourceExtendedAttributes
	preflightAgentSourceExtendedAttributes = func(*os.File) error {
		return errors.New("refusing to replace agent file with content-bound extended attribute \"security.ima\"")
	}
	defer func() { preflightAgentSourceExtendedAttributes = originalPreflight }()

	err = locked.replace([]byte("rewritten private content"))
	if err == nil || !strings.Contains(err.Error(), "content-bound extended attribute") {
		t.Fatalf("replace error=%v, want content-bound-xattr preflight refusal", err)
	}
	content, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "original" {
		t.Fatalf("source content=%q after preflight refusal, want original", content)
	}
	matches, err := filepath.Glob(filepath.Join(dir, ".bv-replace-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("content-bound-xattr preflight left replacement candidates: %v", matches)
	}
}

func TestDarwinProtectionClassIsAppliedBeforeFirstReplacementByte(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Darwin file-protection pre-write regression")
	}
	dir := t.TempDir()
	filePath := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(filePath, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	var sourceStat unix.Stat_t
	if err := unix.Stat(filePath, &sourceStat); err != nil {
		t.Fatal(err)
	}
	type fileKey struct {
		device uint64
		inode  uint64
	}
	keyFor := func(stat unix.Stat_t) fileKey {
		return fileKey{device: uint64(stat.Dev), inode: uint64(stat.Ino)}
	}
	const sourceProtectionClass = 3
	classes := map[fileKey]int{keyFor(sourceStat): sourceProtectionClass}
	setCalls := 0

	originalSupports := agentFileSupportsProtectionClass
	originalFcntl := callAgentProtectionClassFcntl
	agentFileSupportsProtectionClass = func(*os.File) (bool, error) { return true, nil }
	callAgentProtectionClassFcntl = func(fd uintptr, command, argument int) (int, error) {
		var stat unix.Stat_t
		if err := unix.Fstat(int(fd), &stat); err != nil {
			return -1, err
		}
		key := keyFor(stat)
		switch command {
		case 63: // F_GETPROTECTIONCLASS
			return classes[key], nil
		case 64: // F_SETPROTECTIONCLASS
			if stat.Size != 0 {
				return -1, errors.New("protection class was set after replacement bytes existed")
			}
			setCalls++
			classes[key] = argument
			return 0, nil
		default:
			return -1, errors.New("unexpected protection-class fcntl command")
		}
	}
	defer func() {
		agentFileSupportsProtectionClass = originalSupports
		callAgentProtectionClassFcntl = originalFcntl
	}()

	locked, err := lockAgentFileForMutation(filePath)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.close()
	if err := locked.replace([]byte("replacement")); err != nil {
		t.Fatal(err)
	}
	if setCalls != 1 {
		t.Fatalf("Darwin protection-class set calls=%d, want exactly one on the empty candidate", setCalls)
	}
	published, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(published) != "replacement" {
		t.Fatalf("published content=%q, want replacement", published)
	}
}

func TestLinuxReplacementCandidateIsPrivateBeforeWriteAndPreservesSourceMode(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux secure replacement-creation regression")
	}
	dir := t.TempDir()
	filePath := filepath.Join(dir, "AGENTS.md")
	const sourceMode = 0o400
	if err := os.WriteFile(filePath, []byte("original"), sourceMode); err != nil {
		t.Fatal(err)
	}
	locked, err := lockAgentFileForMutation(filePath)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.close()

	candidate, candidatePath, candidateInfo, err := createAgentReplacementFile(locked)
	if err != nil {
		t.Fatal(err)
	}
	pathInfo, err := os.Lstat(candidatePath)
	if err != nil {
		t.Fatal(err)
	}
	if !candidateInfo.Mode().IsRegular() || !pathInfo.Mode().IsRegular() || !os.SameFile(candidateInfo, pathInfo) {
		t.Fatal("candidate handle and path do not identify the same regular file")
	}
	if candidateInfo.Mode().Perm() != 0 || pathInfo.Mode().Perm() != 0 {
		t.Fatalf("new candidate modes handle=%o path=%o, want 000 before writing", candidateInfo.Mode().Perm(), pathInfo.Mode().Perm())
	}
	if candidateInfo.Size() != 0 || pathInfo.Size() != 0 {
		t.Fatalf("new candidate sizes handle=%d path=%d, want zero before writing", candidateInfo.Size(), pathInfo.Size())
	}
	if err := candidate.Close(); err != nil {
		t.Fatal(err)
	}

	if err := locked.replace([]byte("replacement")); err != nil {
		t.Fatal(err)
	}
	publishedInfo, err := os.Stat(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if publishedInfo.Mode().Perm() != sourceMode {
		t.Fatalf("published replacement mode=%o, want source mode %o", publishedInfo.Mode().Perm(), sourceMode)
	}
	published, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(published) != "replacement" {
		t.Fatalf("published replacement content=%q, want replacement", published)
	}
}

func TestLinuxReplacementCandidatePreservesProjectIDBeforeWrite(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux project-quota pre-write regression")
	}
	dir := t.TempDir()
	filePath := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(filePath, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	wantProjectID := installLinuxProjectIDFixture(t, filePath)

	locked, err := lockAgentFileForMutation(filePath)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.close()
	candidate, _, candidateInfo, err := createAgentReplacementFile(locked)
	if err != nil {
		t.Fatal(err)
	}
	defer candidate.Close()
	if candidateInfo.Size() != 0 {
		t.Fatalf("project-quota candidate size=%d, want empty before any replacement write", candidateInfo.Size())
	}
	gotProjectID, err := testLinuxFileProjectID(candidate)
	if err != nil {
		t.Fatalf("read empty replacement project quota ID: %v", err)
	}
	if gotProjectID != wantProjectID {
		t.Fatalf("empty replacement project quota ID=%d, want source ID %d before writing", gotProjectID, wantProjectID)
	}
}

func TestLinuxReplacementPreservesProjectID(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux project-quota publication regression")
	}
	filePath := filepath.Join(t.TempDir(), "AGENTS.md")
	if err := os.WriteFile(filePath, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	wantProjectID := installLinuxProjectIDFixture(t, filePath)

	locked, err := lockAgentFileForMutation(filePath)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.close()
	if err := locked.replace([]byte("replacement")); err != nil {
		t.Fatal(err)
	}
	published, err := os.Open(filePath)
	if err != nil {
		t.Fatal(err)
	}
	defer published.Close()
	gotProjectID, err := testLinuxFileProjectID(published)
	if err != nil {
		t.Fatalf("read published project quota ID: %v", err)
	}
	if gotProjectID != wantProjectID {
		t.Fatalf("published project quota ID=%d, want source ID %d", gotProjectID, wantProjectID)
	}
	content, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "replacement" {
		t.Fatalf("published replacement content=%q, want replacement", content)
	}
}

func TestLinuxReplacementPreservesFullFSXAttrBeforeFirstWrite(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux full-FSXATTR pre-write regression")
	}
	dir := t.TempDir()
	filePath := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(filePath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	want := installLinuxFSXAllocationFixture(t, filePath)

	locked, err := lockAgentFileForMutation(filePath)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.close()
	originalBeforeWrite := beforeAgentReplacementFirstWrite
	observed := false
	beforeAgentReplacementFirstWrite = func(candidate *os.File) error {
		info, err := candidate.Stat()
		if err != nil {
			return err
		}
		if info.Size() != 0 {
			return errors.New("replacement FSXATTR was inspected after bytes existed")
		}
		var got testLinuxFSXAttr
		getRequest, _ := testLinuxFSXAttrIoctlRequests()
		if err := testLinuxFSXAttrIoctl(candidate, getRequest, &got); err != nil {
			return err
		}
		if got.xflags != want.xflags || got.extsSize != want.extsSize ||
			got.projectID != want.projectID || got.cowextsize != want.cowextsize {
			return errors.New("empty replacement FSXATTR policy differs from source")
		}
		observed = true
		return nil
	}
	defer func() { beforeAgentReplacementFirstWrite = originalBeforeWrite }()

	if err := locked.replace([]byte("replacement")); err != nil {
		t.Fatal(err)
	}
	if !observed {
		t.Fatal("replacement FSXATTR policy was not observed before the first write")
	}
	published, err := os.Open(filePath)
	if err != nil {
		t.Fatal(err)
	}
	defer published.Close()
	var got testLinuxFSXAttr
	getRequest, _ := testLinuxFSXAttrIoctlRequests()
	if err := testLinuxFSXAttrIoctl(published, getRequest, &got); err != nil {
		t.Fatal(err)
	}
	if got.xflags != want.xflags || got.extsSize != want.extsSize ||
		got.projectID != want.projectID || got.cowextsize != want.cowextsize {
		t.Fatalf("published FSXATTR=%+v, want source policy %+v", got, want)
	}
}

type testLinuxFSXAttr struct {
	xflags     uint32
	extsSize   uint32
	nextents   uint32
	projectID  uint32
	cowextsize uint32
	pad        [8]byte
}

func installLinuxFSXAllocationFixture(t *testing.T, path string) testLinuxFSXAttr {
	t.Helper()
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	var value testLinuxFSXAttr
	getRequest, setRequest := testLinuxFSXAttrIoctlRequests()
	if err := testLinuxFSXAttrIoctl(file, getRequest, &value); err != nil {
		if linuxProjectIDFixtureUnavailable(err) {
			t.Skipf("filesystem does not expose Linux FSXATTR allocation policy: %v", err)
		}
		t.Fatal(err)
	}
	const (
		testFSXFlagExtentSize    = uint32(0x00000800)
		testFSXFlagCoWExtentSize = uint32(0x00010000)
		testExtentSize           = uint32(64 << 10)
	)
	value.xflags |= testFSXFlagExtentSize | testFSXFlagCoWExtentSize
	value.extsSize = testExtentSize
	value.cowextsize = testExtentSize
	value.nextents = 0
	value.pad = [8]byte{}
	if err := testLinuxFSXAttrIoctl(file, setRequest, &value); err != nil {
		if linuxProjectIDFixtureUnavailable(err) {
			t.Skipf("filesystem does not permit Linux FSXATTR allocation-hint fixture: %v", err)
		}
		t.Fatal(err)
	}
	var verified testLinuxFSXAttr
	if err := testLinuxFSXAttrIoctl(file, getRequest, &verified); err != nil {
		t.Fatal(err)
	}
	if verified.xflags != value.xflags || verified.extsSize != value.extsSize ||
		verified.projectID != value.projectID || verified.cowextsize != value.cowextsize {
		t.Fatalf("installed FSXATTR=%+v, want %+v", verified, value)
	}
	if _, err := file.Write([]byte("original")); err != nil {
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		t.Fatal(err)
	}
	return verified
}

func testLinuxFSXAttrIoctlRequests() (get, set uintptr) {
	payload := uintptr(unsafe.Sizeof(testLinuxFSXAttr{}))<<16 | uintptr('X')<<8
	switch runtime.GOARCH {
	case "mips", "mipsle", "mips64", "mips64le", "ppc", "ppc64", "ppc64le", "sparc64":
		return 0x40000000 | payload | 31, 0x80000000 | payload | 32
	default:
		return 0x80000000 | payload | 31, 0x40000000 | payload | 32
	}
}

func testLinuxFSXAttrIoctl(file *os.File, request uintptr, value *testLinuxFSXAttr) error {
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, file.Fd(), request, uintptr(unsafe.Pointer(value)))
	runtime.KeepAlive(file)
	runtime.KeepAlive(value)
	if errno != 0 {
		return errno
	}
	return nil
}

func testLinuxFileProjectID(file *os.File) (uint32, error) {
	var value testLinuxFSXAttr
	getRequest, _ := testLinuxFSXAttrIoctlRequests()
	if err := testLinuxFSXAttrIoctl(file, getRequest, &value); err != nil {
		return 0, err
	}
	return value.projectID, nil
}

func installLinuxProjectIDFixture(t *testing.T, path string) uint32 {
	t.Helper()
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	var value testLinuxFSXAttr
	getRequest, setRequest := testLinuxFSXAttrIoctlRequests()
	if err := testLinuxFSXAttrIoctl(file, getRequest, &value); err != nil {
		if linuxProjectIDFixtureUnavailable(err) {
			t.Skipf("filesystem does not support a readable project quota ID fixture: %v", err)
		}
		t.Fatalf("read source project quota ID: %v", err)
	}
	const fixtureProjectID = uint32(424242)
	wantProjectID := fixtureProjectID
	if value.projectID == wantProjectID {
		wantProjectID++
	}
	value.projectID = wantProjectID
	if err := testLinuxFSXAttrIoctl(file, setRequest, &value); err != nil {
		if linuxProjectIDFixtureUnavailable(err) {
			t.Skipf("filesystem does not permit a project quota ID fixture: %v", err)
		}
		t.Fatalf("set source project quota ID: %v", err)
	}
	gotProjectID, err := testLinuxFileProjectID(file)
	if err != nil {
		t.Fatalf("verify source project quota ID: %v", err)
	}
	if gotProjectID != wantProjectID {
		t.Fatalf("source project quota ID=%d after fixture setup, want %d", gotProjectID, wantProjectID)
	}
	return wantProjectID
}

func linuxProjectIDFixtureUnavailable(err error) bool {
	return errors.Is(err, unix.ENOTTY) ||
		errors.Is(err, unix.ENOTSUP) ||
		errors.Is(err, unix.EOPNOTSUPP) ||
		errors.Is(err, unix.EINVAL) ||
		errors.Is(err, unix.ENOSYS) ||
		errors.Is(err, unix.EPERM) ||
		errors.Is(err, unix.EACCES)
}

func TestLinuxPrepareDefersAccessACLAndKeepsCandidatePrivate(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux access-ACL preparation regression")
	}
	dir := t.TempDir()
	filePath := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(filePath, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	installLinuxAccessACLFixture(t, filePath)

	locked, err := lockAgentFileForMutation(filePath)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.close()
	candidate, _, _, err := createAgentReplacementFile(locked)
	if err != nil {
		t.Fatal(err)
	}
	defer candidate.Close()
	if _, err := candidate.Write([]byte("rewritten private content")); err != nil {
		t.Fatal(err)
	}
	if err := candidate.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := prepareAgentReplacementMetadata(locked.file, candidate, locked.info.Mode()); err != nil {
		t.Fatal(err)
	}
	info, err := candidate.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0 {
		t.Fatalf("prepared candidate mode=%o, want 000 until access finalization", info.Mode().Perm())
	}
	if _, err := unix.Fgetxattr(int(candidate.Fd()), "system.posix_acl_access", nil); err == nil {
		t.Fatal("prepared private candidate received the source access ACL before finalization")
	}
}

func TestLinuxReplacementPreservesAccessACLOnlyAtFinalization(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux access-ACL preservation regression")
	}
	dir := t.TempDir()
	filePath := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(filePath, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	sourceACL := installLinuxAccessACLFixture(t, filePath)
	sourceInfo, err := os.Stat(filePath)
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

	publishedACL := readLinuxAccessACLFixture(t, filePath)
	if !bytes.Equal(publishedACL, sourceACL) {
		t.Fatalf("published access ACL=%x, want source ACL %x", publishedACL, sourceACL)
	}
	publishedInfo, err := os.Stat(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if publishedInfo.Mode() != sourceInfo.Mode() {
		t.Fatalf("published mode=%s, want source mode %s", publishedInfo.Mode(), sourceInfo.Mode())
	}
	published, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(published) != "replacement" {
		t.Fatalf("published content=%q, want replacement", published)
	}
	displaced, err := filepath.Glob(filepath.Join(dir, ".bv-replace-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(displaced) != 1 {
		t.Fatalf("displaced originals=%v, want exactly one", displaced)
	}
	displacedInfo, err := os.Lstat(displaced[0])
	if err != nil {
		t.Fatal(err)
	}
	if displacedInfo.Mode().Perm() != 0 {
		t.Fatalf("displaced original mode=%o, want 000", displacedInfo.Mode().Perm())
	}
	if _, err := unix.Getxattr(displaced[0], "system.posix_acl_access", nil); err == nil {
		t.Fatal("displaced original retained its source access ACL")
	} else if !errors.Is(err, unix.ENODATA) && !errors.Is(err, unix.ENOTSUP) && !errors.Is(err, unix.EOPNOTSUPP) {
		t.Fatalf("inspect displaced original access ACL: %v", err)
	}
}

func installLinuxAccessACLFixture(t *testing.T, path string) []byte {
	t.Helper()
	const (
		aclVersion  = 2
		aclUserObj  = 0x01
		aclUser     = 0x02
		aclGroupObj = 0x04
		aclMask     = 0x10
		aclOther    = 0x20
		aclUndefID  = ^uint32(0)
	)
	value := make([]byte, 4+5*8)
	binary.LittleEndian.PutUint32(value[:4], aclVersion)
	putEntry := func(index int, tag, permissions uint16, id uint32) {
		offset := 4 + index*8
		binary.LittleEndian.PutUint16(value[offset:offset+2], tag)
		binary.LittleEndian.PutUint16(value[offset+2:offset+4], permissions)
		binary.LittleEndian.PutUint32(value[offset+4:offset+8], id)
	}
	putEntry(0, aclUserObj, 0o6, aclUndefID)
	putEntry(1, aclUser, 0o4, uint32(os.Getuid())+1)
	putEntry(2, aclGroupObj, 0, aclUndefID)
	putEntry(3, aclMask, 0o4, aclUndefID)
	putEntry(4, aclOther, 0, aclUndefID)
	if err := unix.Setxattr(path, "system.posix_acl_access", value, 0); err != nil {
		t.Skipf("filesystem does not support a writable POSIX access ACL fixture: %v", err)
	}
	return readLinuxAccessACLFixture(t, path)
}

func readLinuxAccessACLFixture(t *testing.T, path string) []byte {
	t.Helper()
	size, err := unix.Getxattr(path, "system.posix_acl_access", nil)
	if err != nil {
		t.Fatalf("read POSIX access ACL size: %v", err)
	}
	value := make([]byte, size)
	read, err := unix.Getxattr(path, "system.posix_acl_access", value)
	if err != nil {
		t.Fatalf("read POSIX access ACL: %v", err)
	}
	return value[:read]
}

func TestLinuxNoCOWFlagIsAppliedWhileReplacementCandidateIsEmpty(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux NOCOW pre-write regression")
	}
	dir := t.TempDir()
	filePath := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(filePath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("chattr", "+C", filePath).CombinedOutput(); err != nil {
		t.Skipf("filesystem does not support a writable NOCOW fixture: %v: %s", err, output)
	}
	if !linuxLSAttrContains(t, filePath, 'C') {
		t.Skip("filesystem accepted chattr but did not expose NOCOW")
	}
	if err := os.WriteFile(filePath, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}

	locked, err := lockAgentFileForMutation(filePath)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.close()
	candidate, candidatePath, candidateInfo, err := createAgentReplacementFile(locked)
	if err != nil {
		t.Fatal(err)
	}
	defer candidate.Close()
	if candidateInfo.Size() != 0 {
		t.Fatalf("NOCOW candidate size=%d, want empty before any replacement write", candidateInfo.Size())
	}
	if candidateInfo.Mode().Perm() != 0 {
		t.Fatalf("NOCOW candidate mode=%o, want 000 before any replacement write", candidateInfo.Mode().Perm())
	}
	if !linuxLSAttrContains(t, candidatePath, 'C') {
		t.Fatal("NOCOW was not applied to the empty replacement candidate before writing")
	}
}

func TestWriteReplacementPreservesLinuxNoDumpFlag(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux inode-flag regression")
	}
	filePath := filepath.Join(t.TempDir(), "AGENTS.md")
	if err := os.WriteFile(filePath, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("chattr", "+d", filePath).CombinedOutput(); err != nil {
		t.Skipf("cannot create a Linux nodump fixture: %v: %s", err, output)
	}
	if !linuxLSAttrContains(t, filePath, 'd') {
		t.Skip("filesystem accepted chattr but did not expose nodump")
	}

	locked, err := lockAgentFileForMutation(filePath)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.close()
	if err := locked.replace([]byte("replacement")); err != nil {
		t.Fatal(err)
	}
	if !linuxLSAttrContains(t, filePath, 'd') {
		t.Fatal("Linux nodump flag was lost during replacement")
	}
}

func linuxLSAttrContains(t *testing.T, path string, flag byte) bool {
	t.Helper()
	output, err := exec.Command("lsattr", "-d", path).Output()
	if err != nil {
		t.Fatalf("lsattr failed: %v", err)
	}
	fields := strings.Fields(string(output))
	if len(fields) == 0 {
		t.Fatalf("lsattr returned no fields: %q", output)
	}
	return strings.ContainsRune(fields[0], rune(flag))
}

func TestDarwinReplacementRejectsSourceACLItCannotSecurelyReproduce(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Darwin ACL regression")
	}
	filePath := filepath.Join(t.TempDir(), "AGENTS.md")
	if err := os.WriteFile(filePath, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	userOutput, err := exec.Command("id", "-un").Output()
	if err != nil {
		t.Fatal(err)
	}
	acl := strings.TrimSpace(string(userOutput)) + " allow read"
	if output, err := exec.Command("chmod", "+a", acl, filePath).CombinedOutput(); err != nil {
		t.Skipf("cannot create a Darwin ACL fixture: %v: %s", err, output)
	}
	before := darwinACLListing(t, filePath)
	if before == "" {
		t.Fatal("ACL fixture has no visible ACL entries")
	}

	locked, err := lockAgentFileForMutation(filePath)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.close()
	if err := locked.replace([]byte("replacement")); err == nil || !strings.Contains(err.Error(), "replacement ACL differs") {
		t.Fatalf("replace error=%v, want source-ACL refusal", err)
	}
	after := darwinACLListing(t, filePath)
	if after != before {
		t.Fatalf("ACL changed:\n before: %q\n  after: %q", before, after)
	}
	content, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "original" {
		t.Fatalf("source changed despite source-ACL refusal: %q", content)
	}
	assertRetainedEmptyPrivateReplacement(t, filepath.Dir(filePath))
}

func TestDarwinReplacementRejectsNewlyInheritedACL(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Darwin ACL-inheritance regression")
	}
	dir := t.TempDir()
	filePath := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(filePath, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if before := darwinACLListing(t, filePath); before != "" {
		t.Fatalf("source unexpectedly started with an ACL: %q", before)
	}
	userOutput, err := exec.Command("id", "-un").Output()
	if err != nil {
		t.Fatal(err)
	}
	inheritableACL := strings.TrimSpace(string(userOutput)) + " allow read,file_inherit"
	if output, err := exec.Command("chmod", "+a", inheritableACL, dir).CombinedOutput(); err != nil {
		t.Skipf("cannot create a Darwin inheritable ACL fixture: %v: %s", err, output)
	}

	locked, err := lockAgentFileForMutation(filePath)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.close()
	if err := locked.replace([]byte("replacement")); err == nil || !strings.Contains(err.Error(), "replacement ACL differs") {
		t.Fatalf("replace error=%v, want inherited-ACL refusal", err)
	}
	content, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "original" {
		t.Fatalf("source changed despite inherited-ACL refusal: %q", content)
	}
	assertRetainedEmptyPrivateReplacement(t, dir)
}

func TestDarwinImmutableSourceIsRejectedWithoutReplacementArtifact(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Darwin immutable-flag regression")
	}
	dir := t.TempDir()
	filePath := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(filePath, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("chflags", "uchg", filePath).CombinedOutput(); err != nil {
		t.Skipf("cannot create a Darwin immutable fixture: %v: %s", err, output)
	}
	defer func() {
		if output, err := exec.Command("chflags", "nouchg", filePath).CombinedOutput(); err != nil {
			t.Errorf("clear Darwin immutable fixture: %v: %s", err, output)
		}
	}()

	locked, err := lockAgentFileForMutation(filePath)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.close()
	if err := locked.replace([]byte("replacement")); err == nil || !strings.Contains(err.Error(), "immutable or append-only") {
		t.Fatalf("replace error=%v, want immutable-source refusal", err)
	}
	matches, err := filepath.Glob(filepath.Join(dir, ".bv-replace-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("immutable-source refusal left replacement artifacts: %v", matches)
	}
}

func darwinACLListing(t *testing.T, path string) string {
	t.Helper()
	output, err := exec.Command("ls", "-le", path).Output()
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) <= 1 {
		return ""
	}
	for i := range lines[1:] {
		lines[i+1] = strings.TrimSpace(lines[i+1])
	}
	return strings.Join(lines[1:], "\n")
}

func assertRetainedEmptyPrivateReplacement(t *testing.T, dir string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, ".bv-replace-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("retained replacements=%v, want exactly one fail-closed recovery candidate", matches)
	}
	info, err := os.Stat(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0 {
		t.Fatalf("retained pre-write candidate mode=%o, want 000", info.Mode().Perm())
	}
	if info.Size() != 0 {
		t.Fatalf("retained pre-write candidate size=%d, want 0", info.Size())
	}
}
