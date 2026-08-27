package agents

import (
	"fmt"
	"os"
	"path/filepath"
)

// AgentFileDetection contains the result of detecting an agent config file.
type AgentFileDetection struct {
	// FilePath is the full path to the found file (empty if none found)
	FilePath string

	// FileType is the type of file found ("AGENTS.md", "CLAUDE.md", etc.)
	FileType string

	// HasBlurb indicates whether the file contains a versioned-marker prefix or
	// a detected legacy blurb. Consult BlurbStructureError before mutation.
	HasBlurb bool

	// HasLegacyBlurb indicates the file has the old-format blurb (pre-v1, no HTML markers)
	HasLegacyBlurb bool

	// BlurbVersion is the highest version found (0 if none or legacy).
	BlurbVersion int

	// BlurbCount is the number of complete, structurally valid versioned blocks
	// found before any malformed marker. A healthy injected blurb has count 1.
	BlurbCount int

	// BlurbStructureError describes malformed versioned marker structure or a
	// bounded Markdown analysis that could not safely classify marker text. It is
	// empty only when structural analysis completed without either condition.
	BlurbStructureError string

	// Content is the file content (populated if file was read)
	Content string
}

// Found returns true if an agent file was detected.
func (d AgentFileDetection) Found() bool {
	return d.FilePath != ""
}

// NeedsBlurb returns true if the file exists but doesn't have our blurb.
func (d AgentFileDetection) NeedsBlurb() bool {
	return d.Found() && !d.HasBlurb && !d.HasMalformedBlurb()
}

// HasMalformedBlurb reports whether marker structure is invalid or could not be
// classified safely within the bounded Markdown analysis limits.
func (d AgentFileDetection) HasMalformedBlurb() bool {
	return d.BlurbStructureError != ""
}

// HasDuplicateBlurbs reports whether more than one complete versioned block is
// present. Duplicate current-version blocks still need normalization.
func (d AgentFileDetection) HasDuplicateBlurbs() bool {
	return d.BlurbCount > 1
}

// HasFutureBlurb reports that the file contains instructions written by a
// newer bv binary. Older binaries must never normalize or downgrade them.
func (d AgentFileDetection) HasFutureBlurb() bool {
	return d.HasBlurb && d.BlurbVersion > BlurbVersion
}

// NeedsUpgrade returns true when the blurb needs repair or normalization:
// malformed markers, duplicate versioned blocks, legacy content, or an older
// versioned blurb all require attention.
func (d AgentFileDetection) NeedsUpgrade() bool {
	if d.HasMalformedBlurb() || d.HasDuplicateBlurbs() || d.HasLegacyBlurb {
		return true
	}
	return d.HasBlurb && d.BlurbVersion != BlurbVersion
}

// DetectAgentFile looks for AGENTS.md or CLAUDE.md in the given directory.
// It checks AGENTS.md first (preferred), then falls back to CLAUDE.md.
// The function reads the file content to check for existing blurb markers.
func DetectAgentFile(workDir string) AgentFileDetection {
	// Try each supported file in order of preference
	for _, filename := range SupportedAgentFiles {
		// Only check uppercase variants first (AGENTS.md, CLAUDE.md)
		if filename[0] < 'A' || filename[0] > 'Z' {
			continue
		}

		filePath := filepath.Join(workDir, filename)
		if detection := checkAgentFile(filePath, filename); detection.Found() {
			return detection
		}
	}

	// Try lowercase variants as fallback
	for _, filename := range SupportedAgentFiles {
		if filename[0] >= 'A' && filename[0] <= 'Z' {
			continue
		}

		filePath := filepath.Join(workDir, filename)
		if detection := checkAgentFile(filePath, filename); detection.Found() {
			return detection
		}
	}

	return AgentFileDetection{}
}

// checkAgentFile checks a specific file path for agent configuration.
func checkAgentFile(filePath, fileType string) AgentFileDetection {
	// Inspect the path object itself before opening it. Following a symlink here
	// could disclose arbitrary external file contents through Content.
	info, err := agentFilePathInfo(filePath)
	if err != nil {
		return AgentFileDetection{}
	}
	detection := AgentFileDetection{
		FilePath: filePath,
		FileType: fileType,
	}
	if !info.Mode().IsRegular() {
		return detection
	}

	// Read through an opened handle with the same hard size limit used by the
	// mutation path. os.ReadFile sizes its allocation from mutable path state,
	// so a sparse or concurrently enlarged file could otherwise force an
	// unbounded allocation before EnsureBlurb reaches its guarded write path.
	file, err := openAgentFileForInspectionAtPath(filePath)
	if err != nil {
		// File exists but not readable - return detection without content
		return detection
	}
	defer file.Close()

	openedInfo, err := file.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || !sameAgentFileSnapshot(info, openedInfo) {
		return detection
	}
	content, err := readAgentFileExactly(file, openedInfo.Size())
	if err != nil {
		return detection
	}
	afterInfo, err := file.Stat()
	if err != nil || !sameAgentFileSnapshot(openedInfo, afterInfo) {
		return detection
	}
	currentInfo, err := agentFilePathInfo(filePath)
	if err != nil || !sameAgentFileSnapshot(afterInfo, currentInfo) {
		return detection
	}

	contentStr := string(content)
	hasLegacy := ContainsLegacyBlurb(contentStr)
	originalBlocks, originalStructureErr := inspectBlurbBlocks(contentStr)
	// Remove only an unambiguous historical delimiter. A matching later close
	// makes the adjacent fence a possible user code-block opener, so the real
	// mutation path preserves it byte-for-byte.
	markerContent := contentStr
	var legacyViewErr error
	var ambiguousFencePreserved bool
	var realLegacyRemovals int
	if withoutLegacy, ambiguous, removed, err := removeLegacyBlurbsChecked(contentStr); err != nil {
		legacyViewErr = err
	} else {
		markerContent = withoutLegacy
		ambiguousFencePreserved = ambiguous
		realLegacyRemovals = removed
	}
	markerBlocks, structureErr := inspectBlurbBlocks(markerContent)
	blurbCount := len(markerBlocks)
	blurbVersion := GetBlurbVersion(markerContent)
	completeBlocks := markerBlocks
	completeVersion := highestBlurbBlockVersion(markerBlocks)
	if originalVersion := highestBlurbBlockVersion(originalBlocks); originalVersion > completeVersion {
		completeBlocks = originalBlocks
		completeVersion = originalVersion
	}
	var ambiguityErr error
	// If malformed marker structure may be a historical fence artifact, inspect
	// a hypothetical delimiter-consumed view solely to protect complete future
	// blocks. Never use this view for mutation or for non-future detection: doing
	// so could reinterpret a user's fenced example as installed instructions.
	if ambiguousFencePreserved || originalStructureErr != nil || structureErr != nil {
		if analysisView, _, analysisLegacyRemovals, err := removeLegacyBlurbsCheckedWithPolicy(contentStr, true); err == nil {
			analysisBlocks, analysisStructureErr := inspectBlurbBlocks(analysisView)
			if analysisVersion := highestBlurbBlockVersion(analysisBlocks); analysisVersion > completeVersion {
				completeBlocks = analysisBlocks
				completeVersion = analysisVersion
			}
			if completeVersion <= BlurbVersion && ambiguousFencePreserved && (len(scanBlurbMarkers(markerContent)) > 0 || len(scanBlurbMarkers(analysisView)) > 0) {
				ambiguityErr = fmt.Errorf("malformed bv agent blurb: ambiguous marker material hidden by preserved legacy fence")
			} else if completeVersion <= BlurbVersion && ambiguousFencePreserved && realLegacyRemovals != analysisLegacyRemovals {
				ambiguityErr = fmt.Errorf("malformed legacy bv agent blurb: ambiguous fence changes removal count from %d to %d", realLegacyRemovals, analysisLegacyRemovals)
			} else if completeVersion <= BlurbVersion && analysisStructureErr != nil && originalStructureErr == nil && structureErr == nil {
				structureErr = analysisStructureErr
			}
		}
	}
	if completeVersion > BlurbVersion {
		blurbCount = len(completeBlocks)
		blurbVersion = completeVersion
		ambiguityErr = nil
		originalStructureErr = nil
		structureErr = nil
		legacyViewErr = nil
	}
	structureError := ""
	if ambiguityErr != nil {
		structureError = ambiguityErr.Error()
	} else if originalStructureErr != nil {
		structureError = originalStructureErr.Error()
	} else if structureErr != nil {
		structureError = structureErr.Error()
	} else if legacyViewErr != nil {
		structureError = legacyViewErr.Error()
	}

	return AgentFileDetection{
		FilePath:            filePath,
		FileType:            fileType,
		HasBlurb:            hasLegacy || ContainsBlurb(markerContent),
		HasLegacyBlurb:      hasLegacy,
		BlurbVersion:        blurbVersion,
		BlurbCount:          blurbCount,
		BlurbStructureError: structureError,
		Content:             contentStr,
	}
}

func highestBlurbBlockVersion(blocks []blurbBlock) int {
	version := 0
	for _, block := range blocks {
		if block.version > version {
			version = block.version
		}
	}
	return version
}

// DetectAgentFileInParents searches for agent files starting from workDir
// and walking up the directory tree. This is useful for finding a project-level
// AGENTS.md when running from a subdirectory.
// maxLevels limits how many parent directories to check (0 = current only).
func DetectAgentFileInParents(workDir string, maxLevels int) AgentFileDetection {
	currentDir := workDir
	for i := 0; i <= maxLevels; i++ {
		if detection := DetectAgentFile(currentDir); detection.Found() {
			return detection
		}

		// Move to parent directory
		parentDir := filepath.Dir(currentDir)
		if parentDir == currentDir {
			// Reached root
			break
		}
		currentDir = parentDir
	}

	return AgentFileDetection{}
}

// AgentFileExists checks if any supported agent file exists in the directory.
// This is a quick check without reading file content.
func AgentFileExists(workDir string) bool {
	for _, filename := range SupportedAgentFiles {
		filePath := filepath.Join(workDir, filename)
		if info, err := os.Stat(filePath); err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}

// GetPreferredAgentFilePath returns the path where a new agent file should be created.
// It returns the path for AGENTS.md (preferred format).
func GetPreferredAgentFilePath(workDir string) string {
	return filepath.Join(workDir, "AGENTS.md")
}
