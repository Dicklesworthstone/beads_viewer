//go:build (!darwin && !linux && !windows) || android

package agents

import (
	"fmt"
	"os"
	"runtime"
	"time"
)

type agentFileMetadataSnapshot struct{}

func agentFilePathInfo(path string) (os.FileInfo, error) {
	return os.Lstat(path)
}

func openAgentFileForInspection(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("refusing to inspect non-regular agent file %q", path)
	}
	// A portable Lstat followed by os.Open has an unavoidable swap-to-FIFO or
	// swap-to-symlink race and can block forever. Platforms without a verified
	// nonblocking, no-follow open must fail explicitly instead of pretending the
	// pre-open pathname check made inspection safe.
	return nil, fmt.Errorf("race-safe agent-file inspection is unsupported on %s", runtime.GOOS)
}

func openAndLockAgentFileForMutation(_ string, _ time.Duration) (*os.File, func() error, error) {
	return nil, nil, fmt.Errorf("safe agent-file replacement is unsupported on %s", runtime.GOOS)
}

func snapshotAgentFileMetadata(_ *os.File) (agentFileMetadataSnapshot, error) {
	// Exclusive creation still has handle/path/content stability checks on these
	// targets. Mutation never reaches this fallback because locking is refused.
	return agentFileMetadataSnapshot{}, nil
}

func sameAgentFileMetadata(_, _ agentFileMetadataSnapshot) bool {
	return true
}

func verifyAgentFileHasSingleLink(_ *os.File) error {
	// Go exposes no portable link-count query. The fallback exclusive publisher
	// therefore reports every successful link as partial publication and retains
	// its private source name instead of claiming a single-link postcondition.
	return nil
}

func createAgentReplacementFile(_ *lockedAgentFile) (*os.File, string, os.FileInfo, error) {
	return nil, "", nil, fmt.Errorf("safe agent-file replacement is unsupported on %s", runtime.GOOS)
}

func prepareAgentReplacementMetadata(_, _ *os.File, _ os.FileMode) error {
	return fmt.Errorf("safe agent-file replacement is unsupported on %s", runtime.GOOS)
}

func makeAgentReplacementRemovable(_ *os.File) error {
	return nil
}

func makeAgentReplacementPrivateAfterFailure(_ *os.File) error {
	return nil
}

func finalizeAgentReplacementAccess(_, _ *os.File, _ os.FileMode) error {
	return fmt.Errorf("safe agent-file replacement is unsupported on %s", runtime.GOOS)
}

func publishAgentFileExclusive(sourcePath, destinationPath string) (bool, error) {
	if err := os.Link(sourcePath, destinationPath); err != nil {
		return false, err
	}
	// There is no portable unlink-by-handle primitive. Removing sourcePath after
	// a path identity check would let a peer replace that name in the gap and
	// trick bv into deleting the peer's file. The no-replace destination link is
	// already live, so propagate that publication state while deliberately
	// retaining the cryptorandom private link for explicit recovery.
	return true, fmt.Errorf("destination published; private source link retained at %q because safe identity-bound cleanup is unsupported on %s", sourcePath, runtime.GOOS)
}

func syncAgentParentDirectory(_ string) error {
	return nil
}

func commitAgentReplacement(_ *lockedAgentFile, _ *os.File, _ string, _ os.FileInfo, _ []byte) (bool, error) {
	return false, fmt.Errorf("safe agent-file replacement is unsupported on %s", runtime.GOOS)
}
