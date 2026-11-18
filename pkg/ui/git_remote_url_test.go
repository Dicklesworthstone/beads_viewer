package ui

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

func TestGitRemoteToWebURL(t *testing.T) {
	tests := []struct {
		name   string
		remote string
		want   string
	}{
		{
			name:   "scp style github",
			remote: "git@github.com:owner/repo.git",
			want:   "https://github.com/owner/repo",
		},
		{
			name:   "ssh URL github",
			remote: "ssh://git@github.com/owner/repo.git",
			want:   "https://github.com/owner/repo",
		},
		{
			name:   "ssh URL gitlab nested group",
			remote: "ssh://git@gitlab.com/group/subgroup/repo.git",
			want:   "https://gitlab.com/group/subgroup/repo",
		},
		{
			name:   "ssh URL drops ssh port",
			remote: "ssh://git@github.com:2222/owner/repo.git",
			want:   "https://github.com/owner/repo",
		},
		{
			name:   "https trims suffix and query",
			remote: "https://github.com/owner/repo.git?ignored=1",
			want:   "https://github.com/owner/repo",
		},
		{
			name:   "https strips embedded user and secret token",
			remote: "https://oauth2:ghp_super_secret_token@github.com/owner/repo.git",
			want:   "https://github.com/owner/repo",
		},
		{
			name:   "http strips embedded user and secret password",
			remote: "http://deploy_user:s3cr3tPass!@gitlab.internal:8080/team/service.git",
			want:   "http://gitlab.internal:8080/team/service",
		},
		{
			name:   "empty path rejected",
			remote: "ssh://git@github.com",
			want:   "",
		},
		{
			name:   "unsupported scheme rejected",
			remote: "file:///tmp/repo.git",
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := gitRemoteToWebURL(tt.remote)
			if strings.Compare(got, tt.want) != 0 {
				t.Fatalf("gitRemoteToWebURL(%q) = %q, want %q", tt.remote, got, tt.want)
			}
			if strings.Contains(got, "secret") || strings.Contains(got, "s3cr3t") || strings.Contains(got, "@") {
				t.Fatalf("gitRemoteToWebURL(%q) leaked credentials in %q", tt.remote, got)
			}
		})
	}
}

func TestGetCommitURL_OriginWorkingDirectoryAndSanitizedGitEnv(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	initRepoWithRemote := func(t *testing.T, remoteURL string) string {
		t.Helper()
		dir := t.TempDir()
		for _, args := range [][]string{
			{"init"},
			{"remote", "add", "origin", remoteURL},
		} {
			cmd := exec.Command("git", args...)
			cmd.Dir = dir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %v failed: %v (%s)", args, err, out)
			}
		}
		return dir
	}

	defaultRepo := initRepoWithRemote(t, "https://user:token_default@github.com/org/default-repo.git")
	originRepo := initRepoWithRemote(t, "https://ci:token_origin@github.com/org/member-repo.git")
	poisonRepo := initRepoWithRemote(t, "https://attacker:leak@evil.example/hijack/repo.git")

	issues := []model.Issue{
		{
			ID:        "mem-1",
			Title:     "Workspace member issue",
			Status:    model.StatusOpen,
			Priority:  1,
			IssueType: model.TypeTask,
			Origin: &model.IssueOrigin{
				LocalID:          "1",
				WorkingDirectory: originRepo,
			},
		},
		{
			ID:        "loc-2",
			Title:     "Default workDir issue",
			Status:    model.StatusOpen,
			Priority:  2,
			IssueType: model.TypeTask,
		},
	}

	m := NewModel(issues, nil, "")
	t.Cleanup(m.Stop)
	m.SetWorkDir(defaultRepo)

	// Poison GIT_DIR + GIT_WORK_TREE to point at poisonRepo; SanitizedGitEnv() must strip them.
	t.Setenv("GIT_DIR", filepath.Join(poisonRepo, ".git"))
	t.Setenv("GIT_WORK_TREE", poisonRepo)

	// 1. Selected issue mem-1 has Origin.WorkingDirectory -> uses member-repo, strips credentials, ignores poisoned GIT_DIR.
	m.selectVisibleListItemByID("mem-1")
	gotOriginURL := m.getCommitURL("deadbeef")
	const wantOriginURL = "https://github.com/org/member-repo/commit/deadbeef"
	if gotOriginURL != wantOriginURL {
		t.Fatalf("getCommitURL with Origin.WorkingDirectory = %q, want %q", gotOriginURL, wantOriginURL)
	}

	// 2. Selected issue loc-2 has nil Origin -> falls back to m.workDir (default-repo), still ignoring poisoned GIT_DIR.
	m.selectVisibleListItemByID("loc-2")
	gotDefaultURL := m.getCommitURL("cafebabe")
	const wantDefaultURL = "https://github.com/org/default-repo/commit/cafebabe"
	if gotDefaultURL != wantDefaultURL {
		t.Fatalf("getCommitURL fallback to workDir = %q, want %q", gotDefaultURL, wantDefaultURL)
	}

	// 3. Planted negative controls: blank SHA or non-git directory returns "".
	if got := m.getCommitURL("   "); got != "" {
		t.Fatalf("blank SHA should return empty URL, got %q", got)
	}
	m.SetWorkDir(t.TempDir())
	if got := m.getCommitURL("cafebabe"); got != "" {
		t.Fatalf("non-git workDir should return empty URL (and not read poisoned GIT_DIR), got %q", got)
	}
}
