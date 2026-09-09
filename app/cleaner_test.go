package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsSafeSubpath(t *testing.T) {
	baseDir := "/downloads"

	cases := []struct {
		target   string
		expected bool
	}{
		{"/downloads/test.txt", true},
		{"/downloads/sub/folder/video.mp4", true},
		{"/downloads/../etc/passwd", false},
		{"/etc/passwd", false},
		{"/downloads", false},
		{"/downloads/", false},
		{"/downloads/..", false},
		{"/downloads/sub/../../etc", false},
	}

	for _, c := range cases {
		cleaned, ok := IsSafeSubpath(baseDir, c.target)
		if ok != c.expected {
			t.Errorf("IsSafeSubpath(%q, %q) = (%q, %v), expected %v", baseDir, c.target, cleaned, ok, c.expected)
		}
	}
}

func TestRemoveTaskFiles(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "aria2-cleaner-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	subDir := filepath.Join(tmpDir, "torrent_sub", "nested")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatalf("failed to create nested dir: %v", err)
	}

	file1 := filepath.Join(subDir, "file1.mkv")
	file1Aria := file1 + ".aria2"
	file2 := filepath.Join(tmpDir, "single.iso")
	file2Aria := file2 + ".aria2"

	for _, f := range []string{file1, file1Aria, file2, file2Aria} {
		if err := os.WriteFile(f, []byte("dummy data"), 0644); err != nil {
			t.Fatalf("failed to write %s: %v", f, err)
		}
	}

	deleted, err := RemoveTaskFiles(tmpDir, []string{file1, file2})
	if err != nil {
		t.Fatalf("RemoveTaskFiles failed: %v", err)
	}

	if len(deleted) == 0 {
		t.Errorf("expected deleted files, got empty")
	}

	// file1, file1Aria, file2, file2Aria 应该都不存在
	for _, f := range []string{file1, file1Aria, file2, file2Aria} {
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Errorf("expected %s to be deleted, but it exists", f)
		}
	}

	// nested 和 torrent_sub 应该被递归删除
	if _, err := os.Stat(subDir); !os.IsNotExist(err) {
		t.Errorf("expected %s to be cleaned up as empty dir", subDir)
	}
	parentSub := filepath.Join(tmpDir, "torrent_sub")
	if _, err := os.Stat(parentSub); !os.IsNotExist(err) {
		t.Errorf("expected %s to be cleaned up as empty dir", parentSub)
	}

	// tmpDir 本身不应被删除
	if _, err := os.Stat(tmpDir); os.IsNotExist(err) {
		t.Errorf("expected root download dir %s to still exist", tmpDir)
	}
}
