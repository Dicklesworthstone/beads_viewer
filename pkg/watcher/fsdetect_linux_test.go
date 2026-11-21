//go:build linux

package watcher

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPathWithinMount(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		mountPoint string
		expected   bool
	}{
		{"empty mount", "/foo/bar", "", false},
		{"root mount with root path", "/foo/bar", "/", true},
		{"root mount with non-root path", "foo/bar", "/", false},
		{"exact match", "/mnt/data", "/mnt/data", true},
		{"path within mount", "/mnt/data/subdir/file.txt", "/mnt/data", true},
		{"path outside mount", "/home/user/file.txt", "/mnt/data", false},
		{"similar prefix but different", "/mnt/data2/file.txt", "/mnt/data", false},
		{"mount with trailing slash", "/mnt/data/file.txt", "/mnt/data/", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := pathWithinMount(tc.path, tc.mountPoint); got != tc.expected {
				t.Errorf("pathWithinMount(%q, %q) = %v, expected %v",
					tc.path, tc.mountPoint, got, tc.expected)
			}
		})
	}
}

func TestUnescapeMountField(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"no escapes", "/mnt/data", "/mnt/data"},
		{"space escape", `/mnt/my\040data`, "/mnt/my data"},
		{"tab escape", `/mnt/my\011data`, "/mnt/my\tdata"},
		{"newline escape", `/mnt/my\012data`, "/mnt/my\ndata"},
		{"backslash escape", `/mnt/my\134data`, `/mnt/my\data`},
		{"multiple escapes", `/mnt/my\040special\040path`, "/mnt/my special path"},
		{"all escapes", `/mnt/a\040b\011c\012d\134e`, "/mnt/a b\tc\nd\\e"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := unescapeMountField(tc.input); got != tc.expected {
				t.Errorf("unescapeMountField(%q) = %q, expected %q",
					tc.input, got, tc.expected)
			}
		})
	}
}

func TestDetectFilesystemType_LocalPath(t *testing.T) {
	// Test with a real local path (temp directory)
	tmpDir := t.TempDir()
	fsType := detectFilesystemType(tmpDir)
	// On Linux, statfs on an existing temp directory must classify a concrete type (never Unknown).
	if fsType == FSTypeUnknown {
		t.Fatalf("detectFilesystemType(%q) = FSTypeUnknown, expected concrete Linux filesystem classification", tmpDir)
	}
	if fsType.String() == "" || fsType.String() == "unknown" {
		t.Fatalf("detectFilesystemType(%q) returned invalid FilesystemType string %q", tmpDir, fsType.String())
	}
}

func TestDetectFilesystemType_InvalidPath(t *testing.T) {
	// Non-existent path should return unknown
	fsType := detectFilesystemType("/nonexistent/path/that/does/not/exist")
	if fsType != FSTypeUnknown {
		t.Errorf("detectFilesystemType for invalid path = %v, expected FSTypeUnknown", fsType)
	}
}

func TestIsLinuxSSHFS_InvalidPath(t *testing.T) {
	// Non-existent path should return false (not sshfs)
	result := isLinuxSSHFS("/nonexistent/path")
	if result {
		t.Error("isLinuxSSHFS for invalid path should return false")
	}
}

func TestIsLinuxSSHFS_LocalPath(t *testing.T) {
	// A local temp directory should not be detected as sshfs
	tmpDir := t.TempDir()
	result := isLinuxSSHFS(tmpDir)
	if result {
		t.Error("isLinuxSSHFS for local temp dir should return false")
	}
}

func TestIsLinuxSSHFS_RelativePath(t *testing.T) {
	// Test with relative path to ensure filepath.Abs fallback works
	cwd, err := os.Getwd()
	if err != nil {
		t.Skip("cannot get working directory")
	}
	// Use "." as a relative path
	result := isLinuxSSHFS(".")
	// Should not panic and should return false for local filesystem
	if result {
		t.Errorf("isLinuxSSHFS(\".\") in %s should return false", cwd)
	}
}

func TestIsRemoteFilesystem(t *testing.T) {
	tests := []struct {
		fsType   FilesystemType
		expected bool
	}{
		{FSTypeUnknown, false},
		{FSTypeLocal, false},
		{FSTypeNFS, true},
		{FSTypeSMB, true},
		{FSTypeSSHFS, true},
		{FSTypeFUSE, true},
	}

	for _, tc := range tests {
		t.Run(tc.fsType.String(), func(t *testing.T) {
			if got := isRemoteFilesystem(tc.fsType); got != tc.expected {
				t.Errorf("isRemoteFilesystem(%v) = %v, expected %v",
					tc.fsType, got, tc.expected)
			}
		})
	}
}

func TestDetectFilesystemType_FileVsDirectory(t *testing.T) {
	tmpDir := t.TempDir()

	// Test with directory
	dirType := DetectFilesystemType(tmpDir)

	// Test with file in that directory
	tmpFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(tmpFile, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}
	fileType := DetectFilesystemType(tmpFile)

	// Both should resolve to the same filesystem type since the file's
	// directory is used
	if dirType != fileType {
		t.Errorf("DetectFilesystemType for dir (%v) != file (%v)", dirType, fileType)
	}
}
