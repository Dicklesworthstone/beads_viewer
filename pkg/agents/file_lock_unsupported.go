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
	return nil, fmt.Errorf("race-safe agent-file inspection is unsupported on %s: %s", runtime.GOOS, path)
}

func openAndLockAgentFileForMutation(_ string, _ time.Duration) (*os.File, func() error, error) {
	return nil, nil, fmt.Errorf("safe agent-file replacement is unsupported on %s", runtime.GOOS)
}

func snapshotAgentFileMetadata(_ *os.File) (agentFileMetadataSnapshot, error) {
	return agentFileMetadataSnapshot{}, fmt.Errorf("safe agent-file replacement is unsupported on %s", runtime.GOOS)
}

func sameAgentFileMetadata(_, _ agentFileMetadataSnapshot) bool {
	return false
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

func publishAgentFileExclusive(sourcePath, destinationPath string) error {
	if err := os.Link(sourcePath, destinationPath); err != nil {
		return err
	}
	return os.Remove(sourcePath)
}

func syncAgentParentDirectory(_ string) error {
	return nil
}

func commitAgentReplacement(_ *lockedAgentFile, _ *os.File, _ string, _ os.FileInfo) (bool, error) {
	return false, fmt.Errorf("safe agent-file replacement is unsupported on %s", runtime.GOOS)
}
