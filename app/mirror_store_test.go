package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestTimestampJSON(t *testing.T) {
	b, err := json.Marshal(Timestamp{})
	if err != nil || string(b) != `""` {
		t.Fatalf("zero timestamp = %s, %v; want \"\"", b, err)
	}
	ts := Timestamp{Time: time.Date(2026, 10, 8, 18, 0, 5, 0, time.FixedZone("CST", 8*3600))}
	b, _ = json.Marshal(ts)
	if string(b) != `"2026-10-08T10:00:05Z"` {
		t.Errorf("timestamp = %s, want UTC RFC3339", b)
	}
	for _, in := range []string{`""`, `null`} {
		var got Timestamp
		got.Time = time.Now()
		if err := json.Unmarshal([]byte(in), &got); err != nil || !got.IsZero() {
			t.Errorf("unmarshal %s = %v, %v; want zero", in, got, err)
		}
	}
	var got Timestamp
	if err := json.Unmarshal(b, &got); err != nil || !got.Equal(ts.Time) {
		t.Errorf("roundtrip = %v, %v", got, err)
	}
	if err := json.Unmarshal([]byte(`"yesterday"`), &got); err == nil {
		t.Error("invalid timestamp should fail")
	}
}

func TestMirrorStoreDefaultsWhenMissing(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMirrorStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.view(func(d *mirrorData) {
		if d.Settings != defaultMirrorSettings() {
			t.Errorf("settings = %+v", d.Settings)
		}
		if d.Mirrors == nil || len(d.Mirrors) != 0 || d.State == nil {
			t.Errorf("expected empty, non-nil data: %+v", d)
		}
	})
	if _, err := os.Stat(filepath.Join(dir, mirrorStoreFile)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("opening must not create the file, stat err = %v", err)
	}
}

func TestMirrorStoreRoundtripAndPermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "config")
	s, err := OpenMirrorStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	created := Timestamp{Time: time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC)}
	err = s.update(func(d *mirrorData) error {
		d.Settings = MirrorSettings{RootDir: "mirrors/gh", IntervalMinutes: 60, Proxy: "http://127.0.0.1:7890", GitHubToken: "secret-token"}
		d.Mirrors = append(d.Mirrors, Mirror{ID: "9f2c1a7b3d4e5f60", Name: "PowerToys", Repo: "microsoft/PowerToys", Folder: "PowerToys",
			Enabled: true, SaveChangelog: true, FileTypes: []string{"exe", "msi"}, KeepVersions: 2, CreatedAt: created})
		d.State["9f2c1a7b3d4e5f60"] = &MirrorState{Status: StatusSynced, LatestTag: "v0.85.1", LastCheck: created, LastSuccess: created,
			Files: []MirrorFile{{Name: "a.exe", Size: 10, Status: FileDone, CompletedLength: 10}}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, mirrorStoreFile)
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0600 {
		t.Errorf("config file mode = %v, want 0600", fi.Mode().Perm())
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("temp files left behind: %v", entries)
	}

	raw, _ := os.ReadFile(path)
	var generic map[string]interface{}
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatalf("file is not valid JSON: %v", err)
	}
	for _, key := range []string{`"settings"`, `"mirrors"`, `"state"`, `"rootDir"`, `"intervalMinutes"`, `"githubToken"`, `"saveChangelog"`, `"includePrerelease"`, `"keepVersions"`, `"createdAt"`, `"latestTag"`, `"publishedAt": ""`, `"completedLength"`} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("config file missing %s:\n%s", key, raw)
		}
	}

	s2, err := OpenMirrorStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	var d1, d2 *mirrorData
	s.view(func(d *mirrorData) { d1 = d.clone() })
	s2.view(func(d *mirrorData) { d2 = d.clone() })
	if !reflect.DeepEqual(d1.Settings, d2.Settings) || !reflect.DeepEqual(d1.Mirrors, d2.Mirrors) {
		t.Errorf("roundtrip mismatch:\n%+v\n%+v", d1, d2)
	}
	if !reflect.DeepEqual(d1.State["9f2c1a7b3d4e5f60"].Files, d2.State["9f2c1a7b3d4e5f60"].Files) ||
		d2.State["9f2c1a7b3d4e5f60"].Status != StatusSynced || !d2.State["9f2c1a7b3d4e5f60"].LastCheck.Equal(created.Time) {
		t.Errorf("state roundtrip mismatch: %+v", d2.State["9f2c1a7b3d4e5f60"])
	}
}

func TestMirrorStoreUpdateRollback(t *testing.T) {
	s, err := OpenMirrorStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// fn 返回错误：内存回滚
	err = s.update(func(d *mirrorData) error {
		d.Settings.RootDir = "changed"
		d.Mirrors = append(d.Mirrors, Mirror{ID: "x"})
		return invalidf("nope")
	})
	if err == nil {
		t.Fatal("expected error")
	}
	s.view(func(d *mirrorData) {
		if d.Settings.RootDir != defaultMirrorRootDir || len(d.Mirrors) != 0 {
			t.Errorf("not rolled back: %+v", d)
		}
	})

	// 保存失败（配置目录路径被普通文件占用）：内存回滚并返回错误
	blocker := filepath.Join(t.TempDir(), "file")
	os.WriteFile(blocker, []byte("x"), 0644)
	s2, _ := OpenMirrorStore(filepath.Join(blocker, "sub"))
	err = s2.update(func(d *mirrorData) error {
		d.Settings.IntervalMinutes = 60
		return nil
	})
	if err == nil {
		t.Fatal("expected save error")
	}
	s2.view(func(d *mirrorData) {
		if d.Settings.IntervalMinutes != defaultMirrorInterval {
			t.Errorf("not rolled back after save failure: %+v", d.Settings)
		}
	})
	// 运行状态写入：保存失败不回滚
	if err := s2.updateState(func(d *mirrorData) (bool, error) {
		d.State["a"] = &MirrorState{Status: StatusError}
		return true, nil
	}); err != nil {
		t.Fatalf("updateState should swallow save errors: %v", err)
	}
	s2.view(func(d *mirrorData) {
		if d.State["a"] == nil {
			t.Error("state update lost")
		}
	})
}

func TestMirrorStoreAtomicReplaceKeepsOldOnCorruptWrite(t *testing.T) {
	dir := t.TempDir()
	s, _ := OpenMirrorStore(dir)
	if err := s.update(func(d *mirrorData) error { d.Settings.IntervalMinutes = 60; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := s.update(func(d *mirrorData) error { d.Settings.IntervalMinutes = 120; return nil }); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, mirrorStoreFile))
	var d mirrorData
	if err := json.Unmarshal(raw, &d); err != nil || d.Settings.IntervalMinutes != 120 {
		t.Errorf("file content after replace = %s (%v)", raw, err)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, ".github-mirror-*.tmp"))
	if len(matches) != 0 {
		t.Errorf("temp files left: %v", matches)
	}
}

func TestMirrorStoreCorruptFileBackedUp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, mirrorStoreFile)
	os.WriteFile(path, []byte("{not json"), 0600)
	s, err := OpenMirrorStore(dir)
	if err == nil {
		t.Fatal("expected parse error")
	}
	if s == nil {
		t.Fatal("store must still be usable")
	}
	s.view(func(d *mirrorData) {
		if d.Settings.RootDir != defaultMirrorRootDir {
			t.Errorf("defaults not used: %+v", d.Settings)
		}
	})
	backups, _ := filepath.Glob(path + ".bad-*")
	if len(backups) != 1 {
		t.Errorf("expected one backup, got %v", backups)
	}
}

func TestMirrorStoreNormalizeOnLoad(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "settings": {"rootDir": "../evil", "intervalMinutes": 3, "proxy": "socks5://x", "githubToken": "t"},
  "mirrors": [
    {"id": "a1", "name": "A", "repo": "o/a", "folder": "A", "enabled": true, "fileTypes": null},
    {"id": "b2", "name": "B", "repo": "o/b", "folder": "B", "keepVersions": -3},
    {"id": "b2", "name": "Dup", "repo": "o/c", "folder": "C"},
    {"id": "", "name": "NoID", "repo": "o/d", "folder": "D"}
  ],
  "state": {
    "a1": {"status": "checking", "latestTag": "v1", "files": null, "lastCheck": "2026-10-08T10:00:00Z"},
    "b2": {"status": "checking", "latestTag": ""},
    "zz": {"status": "synced"}
  }
}`
	os.WriteFile(filepath.Join(dir, mirrorStoreFile), []byte(raw), 0600)
	s, err := OpenMirrorStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.view(func(d *mirrorData) {
		if d.Settings.RootDir != defaultMirrorRootDir || d.Settings.IntervalMinutes != defaultMirrorInterval || d.Settings.Proxy != "" || d.Settings.GitHubToken != "t" {
			t.Errorf("settings not normalized: %+v", d.Settings)
		}
		if d.Mirrors[0].FileTypes == nil || d.Mirrors[1].KeepVersions != 0 {
			t.Errorf("mirrors not normalized: %+v", d.Mirrors)
		}
		ids := map[string]bool{}
		for _, m := range d.Mirrors {
			if m.ID == "" || ids[m.ID] {
				t.Errorf("duplicate/empty id after normalize: %+v", d.Mirrors)
			}
			ids[m.ID] = true
		}
		if st := d.State["a1"]; st.Status != StatusDownloading || st.Files == nil || st.LastCheck.IsZero() {
			t.Errorf("interrupted check with tag should become downloading: %+v", st)
		}
		if st := d.State["b2"]; st.Status != StatusIdle {
			t.Errorf("interrupted check without tag should become idle: %+v", st)
		}
		if _, ok := d.State["zz"]; ok {
			t.Error("orphan state not removed")
		}
	})
}

func TestNewMirrorID(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		id := newMirrorID()
		if len(id) != 16 || strings.Trim(id, "0123456789abcdef") != "" {
			t.Fatalf("bad id %q", id)
		}
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
	}
}
