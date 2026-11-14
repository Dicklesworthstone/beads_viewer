package correlation

import (
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/Dicklesworthstone/beads_viewer/pkg/loader"
)

var defaultBeadsFiles = []string{
	".beads/beads.jsonl",
	".beads/issues.jsonl",
	".beads/beads.base.jsonl",
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// deriveStoreDirRel returns the shared git-root-relative slash-normalized directory
// prefix (without leading "./" or trailing "/") for candidates when all non-empty
// candidates live inside the same repository subdirectory (e.g. ".beads",
// "custom-beads", or "services/api/.beads"). Returns "" when candidates live at
// the repository root, escape "..", or do not share a single directory.
func deriveStoreDirRel(candidates []string) string {
	shared := ""
	for _, raw := range candidates {
		if raw == "" {
			continue
		}
		if filepath.IsAbs(raw) {
			return ""
		}
		clean := normalizeRepoSlashPath(raw)
		if clean == "" || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
			return ""
		}
		dir := path.Dir(clean)
		if dir == "" || dir == "." || dir == ".." || strings.HasPrefix(dir, "../") {
			return ""
		}
		if shared == "" {
			shared = dir
		} else if shared != dir {
			return ""
		}
	}
	return shared
}

func pickBeadsFiles(repoPath string, candidates []string) []string {
	if len(candidates) == 0 {
		return nil
	}
	candidates = orderBeadsFilesForWorkspace(repoPath, candidates)

	primary := ""
	for _, rel := range candidates {
		if rel == "" {
			continue
		}
		if fileExists(filepath.Join(repoPath, filepath.FromSlash(rel))) {
			primary = rel
			break
		}
	}
	if primary == "" {
		return candidates
	}

	out := make([]string, 0, len(candidates))
	out = append(out, primary)
	for _, rel := range candidates {
		if rel == primary {
			continue
		}
		out = append(out, rel)
	}
	return out
}

func orderBeadsFilesForWorkspace(repoPath string, candidates []string) []string {
	storeDirRel := deriveStoreDirRel(candidates)
	if storeDirRel == "" {
		storeDirRel = ".beads"
	}
	if !loader.IsBDWorkspace(filepath.Join(repoPath, filepath.FromSlash(storeDirRel))) {
		return candidates
	}
	return promoteBeadsFile(path.Join(storeDirRel, "issues.jsonl"), candidates)
}

func promoteBeadsFile(primary string, candidates []string) []string {
	for _, rel := range candidates {
		if rel == primary {
			return prependBeadsFile(primary, candidates)
		}
	}
	return candidates
}

func prependBeadsFile(primary string, candidates []string) []string {
	if primary == "" {
		return candidates
	}
	out := []string{primary}
	for _, rel := range candidates {
		if rel == primary {
			continue
		}
		out = append(out, rel)
	}
	return out
}
