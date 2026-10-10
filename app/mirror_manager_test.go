package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSyncFullFlow(t *testing.T) {
	env := newTestEnv(t, nil)
	m, gh, a2 := env.m, env.gh, env.a2
	mv := env.addMirror(t, MirrorInput{Name: "PowerToys", Repo: "microsoft/PowerToys", Folder: "PowerToys", FileTypes: flexStrings{"exe", ".MSI"}})

	dl := gh.srv.URL + "/dl"
	gh.setJSON("/repos/microsoft/PowerToys/releases/latest", testRelease("v0.85.1", "## What's Changed\n- fix", dl,
		testAsset{"Setup-x64.exe", 10},
		testAsset{"Setup-arm64.EXE", 5},
		testAsset{"Tool.msi", 7},
		testAsset{"source.zip", 3},
		testAsset{"Partial.exe", 8},
	))

	versionDir := filepath.Join(env.folderDir("PowerToys"), "v0.85.1")
	writeTestFile(t, filepath.Join(versionDir, "Setup-x64.exe"), 10) // 已完成
	writeTestFile(t, filepath.Join(versionDir, "Partial.exe"), 8)    // 预分配大小但仍有控制文件
	writeTestFile(t, filepath.Join(versionDir, "Partial.exe.aria2"), 1)
	a2.setWaiting(fakeTask{path: filepath.Join(versionDir, "Partial.exe"), completed: 3, total: 8})
	a2.setActive(fakeTask{path: filepath.Join(versionDir, "Tool.msi"), completed: 4, total: 7})

	if err := m.Sync(context.Background(), mv.ID); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	calls := a2.calls()
	if len(calls) != 1 {
		t.Fatalf("expected exactly 1 addUri call, got %d: %+v", len(calls), calls)
	}
	if !reflect.DeepEqual(calls[0].uris, []string{dl + "/Setup-arm64.EXE"}) {
		t.Errorf("addUri uris = %v", calls[0].uris)
	}
	wantOpts := map[string]string{
		"dir": versionDir, "out": "Setup-arm64.EXE", "allow-overwrite": "true", "auto-file-renaming": "false",
		"continue": "false", "follow-metalink": "false", "follow-torrent": "false",
	}
	if !reflect.DeepEqual(calls[0].options, wantOpts) {
		t.Errorf("addUri options = %v, want %v", calls[0].options, wantOpts)
	}

	changelog := filepath.Join(versionDir, "CHANGELOG.md")
	if b, err := os.ReadFile(changelog); err != nil || string(b) != "## What's Changed\n- fix" {
		t.Errorf("CHANGELOG = %q, %v", b, err)
	}

	st := env.state(mv.ID)
	if st.Status != StatusDownloading || st.LatestTag != "v0.85.1" || st.ReleaseName != "Release v0.85.1" ||
		st.ReleaseURL != "https://github.com/o/r/releases/tag/v0.85.1" || st.LastError != "" {
		t.Errorf("unexpected state: %+v", st)
	}
	if !st.LastCheck.Equal(env.clock.Now()) || !st.LastSuccess.IsZero() {
		t.Errorf("lastCheck=%v lastSuccess=%v", st.LastCheck, st.LastSuccess)
	}
	if !st.PublishedAt.Equal(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("publishedAt = %v", st.PublishedAt)
	}
	wantFiles := []MirrorFile{
		{Name: "Setup-x64.exe", Size: 10, Status: FileDone, CompletedLength: 10},
		{Name: "Setup-arm64.EXE", Size: 5, Status: FileDownloading, CompletedLength: 0},
		{Name: "Tool.msi", Size: 7, Status: FileDownloading, CompletedLength: 4},
		{Name: "Partial.exe", Size: 8, Status: FileDownloading, CompletedLength: 3},
	}
	if !reflect.DeepEqual(st.Files, wantFiles) {
		t.Errorf("files = %+v\nwant %+v", st.Files, wantFiles)
	}
	req := gh.lastRequest()
	if req.header.Get("Authorization") != "" || req.header.Get("User-Agent") != "aria2-one" {
		t.Errorf("unexpected GitHub headers: %v", req.header)
	}

	// 进度刷新：仍在下载，completedLength 更新
	a2.setActive(fakeTask{path: filepath.Join(versionDir, "Tool.msi"), completed: 6, total: 7},
		fakeTask{path: filepath.Join(versionDir, "Setup-arm64.EXE"), completed: 2, total: 5})
	if err := m.RefreshProgress(context.Background()); err != nil {
		t.Fatal(err)
	}
	st = env.state(mv.ID)
	if st.Status != StatusDownloading || st.Files[2].CompletedLength != 6 || st.Files[1].CompletedLength != 2 {
		t.Errorf("progress not refreshed: %+v", st)
	}

	// 全部下载完成 → synced
	writeTestFile(t, filepath.Join(versionDir, "Setup-arm64.EXE"), 5)
	writeTestFile(t, filepath.Join(versionDir, "Tool.msi"), 7)
	os.Remove(filepath.Join(versionDir, "Partial.exe.aria2"))
	a2.setActive()
	a2.setWaiting()
	env.clock.Advance(time.Minute)
	if err := m.RefreshProgress(context.Background()); err != nil {
		t.Fatal(err)
	}
	st = env.state(mv.ID)
	if st.Status != StatusSynced || !st.LastSuccess.Equal(env.clock.Now()) {
		t.Errorf("expected synced with lastSuccess, got %+v", st)
	}
	for _, f := range st.Files {
		if f.Status != FileDone || f.CompletedLength != f.Size {
			t.Errorf("file not done: %+v", f)
		}
	}

	// 状态已持久化
	s2, err := OpenMirrorStore(m.cfg.ConfigDir)
	if err != nil {
		t.Fatal(err)
	}
	s2.view(func(d *mirrorData) {
		if d.State[mv.ID] == nil || d.State[mv.ID].Status != StatusSynced {
			t.Errorf("synced state not persisted: %+v", d.State[mv.ID])
		}
	})

	// 再次同步：不重复投递、不覆盖已有 CHANGELOG
	os.WriteFile(changelog, []byte("local edit"), 0644)
	env.clock.Advance(time.Hour)
	if err := m.Sync(context.Background(), mv.ID); err != nil {
		t.Fatal(err)
	}
	if n := len(a2.calls()); n != 1 {
		t.Errorf("re-sync queued %d new downloads", n-1)
	}
	if b, _ := os.ReadFile(changelog); string(b) != "local edit" {
		t.Errorf("CHANGELOG overwritten: %q", b)
	}
	st = env.state(mv.ID)
	if st.Status != StatusSynced || !st.LastSuccess.Equal(env.clock.Now()) || !st.LastCheck.Equal(env.clock.Now()) {
		t.Errorf("re-sync state = %+v", st)
	}
}

func TestSyncIncompleteWhenNothingInProgress(t *testing.T) {
	env := newTestEnv(t, nil)
	mv := env.addMirror(t, MirrorInput{Repo: "o/r", SaveChangelog: boolPtr(false)})
	env.gh.setJSON("/repos/o/r/releases/latest", testRelease("v1", "body", "http://dl", testAsset{"a.bin", 4}))
	if err := env.m.Sync(context.Background(), mv.ID); err != nil {
		t.Fatal(err)
	}
	vdir := filepath.Join(env.folderDir("r"), "v1")
	if _, err := os.Stat(filepath.Join(vdir, "CHANGELOG.md")); !os.IsNotExist(err) {
		t.Error("CHANGELOG written although saveChangelog=false")
	}
	if st := env.state(mv.ID); st.Status != StatusDownloading {
		t.Fatalf("status = %s", st.Status)
	}
	// aria2 任务失败（不在进行中列表）且文件不完整 → incomplete
	writeTestFile(t, filepath.Join(vdir, "a.bin"), 2)
	if err := env.m.RefreshProgress(context.Background()); err != nil {
		t.Fatal(err)
	}
	st := env.state(mv.ID)
	if st.Status != StatusIncomplete || st.Files[0].Status != FilePending || !st.LastSuccess.IsZero() {
		t.Errorf("expected incomplete/pending, got %+v", st)
	}
	// incomplete 不再被 RefreshProgress 处理，下一次同步会重新投递
	if err := env.m.Sync(context.Background(), mv.ID); err != nil {
		t.Fatal(err)
	}
	if n := len(env.a2.calls()); n != 2 {
		t.Errorf("expected re-queue on next sync, addUri calls = %d", n)
	}
}

func TestSyncEmptyFilterResultIsSynced(t *testing.T) {
	env := newTestEnv(t, nil)
	mv := env.addMirror(t, MirrorInput{Repo: "o/r", FileTypes: flexStrings{"apk"}})
	env.gh.setJSON("/repos/o/r/releases/latest", testRelease("v1", "", "http://dl", testAsset{"a.exe", 4}))
	if err := env.m.Sync(context.Background(), mv.ID); err != nil {
		t.Fatal(err)
	}
	st := env.state(mv.ID)
	if st.Status != StatusSynced || len(st.Files) != 0 || st.Files == nil {
		t.Errorf("state = %+v", st)
	}
	if len(env.a2.calls()) != 0 {
		t.Error("nothing should be queued")
	}
}

func TestSyncWithProxySetting(t *testing.T) {
	// GitHub API 地址不可达，只有经过设置中的代理（即 mock 服务器）才能成功
	env := newTestEnv(t, func(c *ManagerConfig) { c.GitHubAPI = "http://api.github.invalid" })
	if _, err := env.m.UpdateSettings(SettingsInput{Proxy: strPtr(env.gh.srv.URL)}); err != nil {
		t.Fatal(err)
	}
	mv := env.addMirror(t, MirrorInput{Repo: "o/r"})
	env.gh.setJSON("/repos/o/r/releases/latest", testRelease("v1", "", "http://dl", testAsset{"a.bin", 4}))
	if err := env.m.Sync(context.Background(), mv.ID); err != nil {
		t.Fatalf("Sync via proxy: %v", err)
	}
	calls := env.a2.calls()
	if len(calls) != 1 || calls[0].options["all-proxy"] != env.gh.srv.URL {
		t.Errorf("all-proxy not passed to aria2: %+v", calls)
	}
	if len(calls[0].options) != 8 {
		t.Errorf("unexpected addUri options: %v", calls[0].options)
	}
}

func TestSyncTokenPrecedence(t *testing.T) {
	cases := []struct {
		settingsToken, envToken, want string
	}{
		{"tok-settings", "tok-env", "Bearer tok-settings"},
		{"", "tok-env", "Bearer tok-env"},
		{"", "", ""},
	}
	for _, c := range cases {
		env := newTestEnv(t, func(cfg *ManagerConfig) { cfg.EnvToken = c.envToken })
		if c.settingsToken != "" {
			env.m.UpdateSettings(SettingsInput{GitHubToken: strPtr(c.settingsToken)})
		}
		mv := env.addMirror(t, MirrorInput{Repo: "o/r"})
		env.gh.setJSON("/repos/o/r/releases/latest", testRelease("v1", "", "http://dl"))
		if err := env.m.Sync(context.Background(), mv.ID); err != nil {
			t.Fatal(err)
		}
		if got := env.gh.lastRequest().header.Get("Authorization"); got != c.want {
			t.Errorf("settings=%q env=%q: Authorization = %q, want %q", c.settingsToken, c.envToken, got, c.want)
		}
	}
}

func TestSyncErrors(t *testing.T) {
	reset := time.Date(2026, 10, 8, 11, 23, 0, 0, time.UTC)
	cases := []struct {
		name  string
		setup func(env *testEnv)
		input MirrorInput
		want  string
	}{
		{
			name:  "404",
			setup: func(env *testEnv) {},
			want:  "仓库不存在或没有正式 Release",
		},
		{
			name: "rate limit",
			setup: func(env *testEnv) {
				env.gh.set("/repos/o/r/releases/latest", 403, `{"message":"API rate limit exceeded"}`,
					map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": strconv.FormatInt(reset.Unix(), 10)})
			},
			want: "GitHub API 限流，将于 11:23 重置，建议在设置中填写 Token",
		},
		{
			name: "server error message",
			setup: func(env *testEnv) {
				env.gh.set("/repos/o/r/releases/latest", 500, `{"message":"Server Error"}`, nil)
			},
			want: "GitHub API 返回 500：Server Error",
		},
		{
			name: "addUri failure",
			setup: func(env *testEnv) {
				env.gh.setJSON("/repos/o/r/releases/latest", testRelease("v1", "", "http://dl", testAsset{"a.bin", 1}, testAsset{"b.bin", 1}))
				env.a2.setAddErr("disk full")
			},
			want: "a.bin：aria2.addUri 失败：disk full",
		},
		{
			name: "unsafe tag",
			setup: func(env *testEnv) {
				env.gh.setJSON("/repos/o/r/releases/latest", testRelease("..", "", "http://dl"))
			},
			want: "Release tag 无法作为目录名",
		},
		{
			name: "aria2 unreachable",
			setup: func(env *testEnv) {
				env.gh.setJSON("/repos/o/r/releases/latest", testRelease("v1", "", "http://dl", testAsset{"a.bin", 1}))
				env.a2.srv.Close()
			},
			want: "查询 aria2 下载任务失败",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := newTestEnv(t, nil)
			in := c.input
			if in.Repo == "" {
				in.Repo = "o/r"
			}
			mv := env.addMirror(t, in)
			c.setup(env)
			err := env.m.Sync(context.Background(), mv.ID)
			if err == nil {
				t.Fatal("expected error")
			}
			st := env.state(mv.ID)
			if st.Status != StatusError || !strings.Contains(st.LastError, c.want) {
				t.Errorf("state = %s / %q, want error containing %q", st.Status, st.LastError, c.want)
			}
			if st.LastCheck.IsZero() {
				t.Error("lastCheck not set")
			}
		})
	}
}

func TestSyncAddURIFailureMarksPending(t *testing.T) {
	env := newTestEnv(t, nil)
	mv := env.addMirror(t, MirrorInput{Repo: "o/r"})
	env.gh.setJSON("/repos/o/r/releases/latest", testRelease("v1", "", "http://dl", testAsset{"a.bin", 1}))
	env.a2.setAddErr("boom")
	env.m.Sync(context.Background(), mv.ID)
	st := env.state(mv.ID)
	if st.Status != StatusError || st.LatestTag != "v1" || len(st.Files) != 1 || st.Files[0].Status != FilePending {
		t.Errorf("state = %+v", st)
	}
	if st.LastError != "部分文件投递到 aria2 失败：a.bin：aria2.addUri 失败：boom" || !strings.Contains(st.Files[0].Error, "boom") {
		t.Errorf("lastError = %q, file error = %q", st.LastError, st.Files[0].Error)
	}
	// 恢复后再次同步：错误清空
	env.a2.setAddErr("")
	if err := env.m.Sync(context.Background(), mv.ID); err != nil {
		t.Fatal(err)
	}
	if st := env.state(mv.ID); st.Status != StatusDownloading || st.LastError != "" || st.Files[0].Error != "" {
		t.Errorf("state after recovery = %+v", st)
	}
}

func TestSyncErrorKeepsPreviousRelease(t *testing.T) {
	env := newTestEnv(t, nil)
	mv := env.addMirror(t, MirrorInput{Repo: "o/r"})
	env.gh.setJSON("/repos/o/r/releases/latest", testRelease("v1", "", "http://dl"))
	if err := env.m.Sync(context.Background(), mv.ID); err != nil {
		t.Fatal(err)
	}
	env.gh.set("/repos/o/r/releases/latest", 404, `{}`, nil)
	env.m.Sync(context.Background(), mv.ID)
	st := env.state(mv.ID)
	if st.Status != StatusError || st.LatestTag != "v1" || st.LastSuccess.IsZero() {
		t.Errorf("state = %+v", st)
	}
}

func TestSyncPrereleaseSelection(t *testing.T) {
	env := newTestEnv(t, nil)
	mv := env.addMirror(t, MirrorInput{Repo: "o/r", IncludePrerelease: true})
	draft := testRelease("v3.0.0", "", "http://dl")
	draft["draft"] = true
	pre := testRelease("v2.0.0-rc.1", "", "http://dl", testAsset{"rc.bin", 1})
	pre["prerelease"] = true
	env.gh.setJSON("/repos/o/r/releases?per_page=10", []interface{}{draft, pre, testRelease("v1.0.0", "", "http://dl")})
	if err := env.m.Sync(context.Background(), mv.ID); err != nil {
		t.Fatal(err)
	}
	if st := env.state(mv.ID); st.LatestTag != "v2.0.0-rc.1" {
		t.Errorf("latestTag = %q", st.LatestTag)
	}
	if env.gh.requestsFor("/repos/o/r/releases/latest") != 0 {
		t.Error("includePrerelease must not use /releases/latest")
	}
	if _, err := os.Stat(filepath.Join(env.folderDir("r"), "v2.0.0-rc.1")); err != nil {
		t.Errorf("version dir missing: %v", err)
	}
}

func TestSyncSanitizesTagAndAssetNames(t *testing.T) {
	env := newTestEnv(t, nil)
	mv := env.addMirror(t, MirrorInput{Repo: "o/r"})
	env.gh.setJSON("/repos/o/r/releases/latest", testRelease("release/1.0", "", "http://dl", testAsset{"..", 1}, testAsset{"ok.bin", 1}))
	if err := env.m.Sync(context.Background(), mv.ID); err != nil {
		t.Fatal(err)
	}
	vdir := filepath.Join(env.folderDir("r"), "release_1.0")
	if _, err := os.Stat(vdir); err != nil {
		t.Errorf("sanitized version dir missing: %v", err)
	}
	calls := env.a2.calls()
	if len(calls) != 1 || calls[0].options["out"] != "ok.bin" || calls[0].options["dir"] != vdir {
		t.Errorf("unexpected addUri calls: %+v", calls)
	}
}

func TestSyncUnknownMirror(t *testing.T) {
	env := newTestEnv(t, nil)
	if err := env.m.Sync(context.Background(), "nope"); !errors.Is(err, errMirrorNotFound) {
		t.Errorf("err = %v", err)
	}
}

func TestSyncSingleFlight(t *testing.T) {
	env := newTestEnv(t, nil)
	mv := env.addMirror(t, MirrorInput{Repo: "o/r"})
	env.gh.setJSON("/repos/o/r/releases/latest", testRelease("v1", "", "http://dl"))
	block := make(chan struct{})
	env.gh.setBlock(block)

	done := make(chan error, 1)
	go func() { done <- env.m.Sync(context.Background(), mv.ID) }()
	waitFor(t, "first GitHub request", func() bool { return env.gh.requestCount() == 1 })

	if st := env.state(mv.ID); st.Status != StatusChecking {
		t.Errorf("status during sync = %s, want checking", st.Status)
	}
	if err := env.m.Sync(context.Background(), mv.ID); !errors.Is(err, errSyncInProgress) {
		t.Errorf("concurrent Sync err = %v, want errSyncInProgress", err)
	}
	env.m.SyncAsync(mv.ID) // 也应被忽略
	env.gh.setBlock(nil)
	close(block)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	env.m.Wait()
	if n := env.gh.requestCount(); n > 2 {
		t.Errorf("GitHub requests = %d", n)
	}
	if st := env.state(mv.ID); st.Status != StatusSynced {
		t.Errorf("final status = %s", st.Status)
	}
}

func TestRefreshProgressSkipsSyncingAndDiscardsStale(t *testing.T) {
	env := newTestEnv(t, nil)
	mv := env.addMirror(t, MirrorInput{Repo: "o/r"})
	env.gh.setJSON("/repos/o/r/releases/latest", testRelease("v1", "", "http://dl", testAsset{"a.bin", 1}))
	if err := env.m.Sync(context.Background(), mv.ID); err != nil {
		t.Fatal(err)
	}
	// 文件不在下载中也不完整：刷新后本应变为 incomplete

	// 1. 正在同步的镜像会被跳过
	env.m.tryBeginSync(context.Background(), mv.ID)
	if err := env.m.RefreshProgress(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := env.state(mv.ID); st.Status != StatusDownloading {
		t.Errorf("syncing mirror should be skipped, status = %s", st.Status)
	}
	env.m.endSync(mv.ID)

	// 2. 读取之后、写回之前有新的同步写入（rev 变化）：丢弃本次结果
	env.m.refreshHook = func() {
		env.m.store.updateState(func(d *mirrorData) (bool, error) {
			d.State[mv.ID].rev++
			return false, nil
		})
	}
	if err := env.m.RefreshProgress(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := env.state(mv.ID); st.Status != StatusDownloading {
		t.Errorf("stale refresh result should be discarded, status = %s", st.Status)
	}
	env.m.refreshHook = nil

	// 3. 正常刷新
	if err := env.m.RefreshProgress(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := env.state(mv.ID); st.Status != StatusIncomplete {
		t.Errorf("status = %s, want incomplete", st.Status)
	}
}

func TestRefreshProgressAria2Down(t *testing.T) {
	env := newTestEnv(t, nil)
	mv := env.addMirror(t, MirrorInput{Repo: "o/r"})
	env.gh.setJSON("/repos/o/r/releases/latest", testRelease("v1", "", "http://dl", testAsset{"a.bin", 1}))
	if err := env.m.Sync(context.Background(), mv.ID); err != nil {
		t.Fatal(err)
	}
	env.a2.srv.Close()
	if err := env.m.RefreshProgress(context.Background()); err == nil {
		t.Error("expected error when aria2 is unreachable")
	}
	if st := env.state(mv.ID); st.Status != StatusDownloading {
		t.Errorf("state must be unchanged when aria2 is unreachable, got %s", st.Status)
	}
}

func TestRetentionKeepVersions(t *testing.T) {
	env := newTestEnv(t, nil)
	mv := env.addMirror(t, MirrorInput{Repo: "o/r", Folder: "App", KeepVersions: 2})
	folder := env.folderDir("App")
	now := time.Now()
	for i, tag := range []string{"v1", "v2", "v3"} {
		writeTestFile(t, filepath.Join(folder, tag, "f.bin"), 1)
		mt := now.Add(time.Duration(i-3) * time.Hour)
		os.Chtimes(filepath.Join(folder, tag), mt, mt)
	}
	writeTestFile(t, filepath.Join(folder, "notes.txt"), 1)
	// v1 最旧但仍有 aria2 任务，不得删除
	env.a2.setActive(fakeTask{path: filepath.Join(folder, "v1", "f.bin"), completed: 0, total: 1})

	env.gh.setJSON("/repos/o/r/releases/latest", testRelease("v4", "", "http://dl"))
	if err := env.m.Sync(context.Background(), mv.ID); err != nil {
		t.Fatal(err)
	}
	exists := func(name string) bool {
		_, err := os.Stat(filepath.Join(folder, name))
		return err == nil
	}
	if !exists("v4") || !exists("v3") {
		t.Error("current and newest old version must be kept")
	}
	if exists("v2") {
		t.Error("v2 should have been removed")
	}
	if !exists("v1") {
		t.Error("v1 has an in-progress download and must be kept")
	}
	if !exists("notes.txt") {
		t.Error("regular files in the folder must not be touched")
	}

	// keepVersions=1：只保留当前版本
	env.a2.setActive()
	in := MirrorInput{Repo: "o/r", Folder: "App", KeepVersions: 1}
	if _, err := env.m.UpdateMirror(mv.ID, in); err != nil {
		t.Fatal(err)
	}
	if err := env.m.Sync(context.Background(), mv.ID); err != nil {
		t.Fatal(err)
	}
	if exists("v1") || exists("v3") || !exists("v4") {
		t.Error("keepVersions=1 should keep only the current version")
	}

	// keepVersions=0：不删除
	writeTestFile(t, filepath.Join(folder, "v0", "f.bin"), 1)
	in.KeepVersions = 0
	env.m.UpdateMirror(mv.ID, in)
	env.m.Sync(context.Background(), mv.ID)
	if !exists("v0") {
		t.Error("keepVersions=0 must keep all versions")
	}
}

func TestRetentionDoesNotTouchOtherFolders(t *testing.T) {
	env := newTestEnv(t, nil)
	mv := env.addMirror(t, MirrorInput{Repo: "o/r", Folder: "App", KeepVersions: 1})
	other := filepath.Join(env.folderDir("Other"), "v1", "x.bin")
	writeTestFile(t, other, 1)
	outside := filepath.Join(env.m.downloadDir, "user-file.iso")
	writeTestFile(t, outside, 1)
	env.gh.setJSON("/repos/o/r/releases/latest", testRelease("v2", "", "http://dl"))
	if err := env.m.Sync(context.Background(), mv.ID); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{other, outside} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s removed by retention", p)
		}
	}
}

func TestIsManagedPath(t *testing.T) {
	env := newTestEnv(t, nil)
	dl := env.m.downloadDir
	cases := []struct {
		p    string
		want bool
	}{
		{filepath.Join(dl, "github-mirror", "App", "v1", "a.exe"), true},
		{filepath.Join(dl, "github-mirror", "App"), true},
		{filepath.Join(dl, "github-mirror"), false},
		{filepath.Join(dl, "github-mirror-other", "a.exe"), false},
		{filepath.Join(dl, "movie.mkv"), false},
		{dl + "/github-mirror/../movie.mkv", false},
		{"", false},
		{"/etc/passwd", false},
	}
	for _, c := range cases {
		if got := env.m.IsManagedPath(c.p); got != c.want {
			t.Errorf("IsManagedPath(%q) = %v, want %v", c.p, got, c.want)
		}
	}
	env.m.UpdateSettings(SettingsInput{RootDir: strPtr("mirrors/gh")})
	if !env.m.IsManagedPath(filepath.Join(dl, "mirrors", "gh", "App", "a.exe")) {
		t.Error("new rootDir not protected")
	}
	if env.m.IsManagedPath(filepath.Join(dl, "github-mirror", "App", "a.exe")) {
		t.Error("old rootDir should no longer be protected")
	}
	if env.m.IsManagedPath(filepath.Join(dl, "mirrors", "other.bin")) {
		t.Error("parent of rootDir must not be protected")
	}
}

func TestDueMirrors(t *testing.T) {
	env := newTestEnv(t, nil)
	now := env.clock.Now()
	never := env.addMirror(t, MirrorInput{Repo: "o/never", Enabled: boolPtr(true)})
	recent := env.addMirror(t, MirrorInput{Repo: "o/recent", Enabled: boolPtr(true)})
	old := env.addMirror(t, MirrorInput{Repo: "o/old", Enabled: boolPtr(true)})
	future := env.addMirror(t, MirrorInput{Repo: "o/future", Enabled: boolPtr(true)})
	disabled := env.addMirror(t, MirrorInput{Repo: "o/disabled"})
	env.m.Close() // 停止创建时触发的异步同步，避免影响状态
	env.m.store.updateState(func(d *mirrorData) (bool, error) {
		d.State[never.ID] = newIdleState()
		d.stateFor(recent.ID).LastCheck = Timestamp{Time: now.Add(-359 * time.Minute)}
		d.stateFor(old.ID).LastCheck = Timestamp{Time: now.Add(-360 * time.Minute)}
		d.stateFor(future.ID).LastCheck = Timestamp{Time: now.Add(24 * time.Hour)}
		d.stateFor(disabled.ID).LastCheck = Timestamp{}
		return false, nil
	})
	got := env.m.dueMirrors(now)
	want := []string{never.ID, old.ID, future.ID}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("due = %v, want %v", got, want)
	}
	env.m.UpdateSettings(SettingsInput{IntervalMinutes: func() *flexInt { n := flexInt(0); return &n }()})
	if got := env.m.dueMirrors(now); len(got) != 0 {
		t.Errorf("interval 0 (manual only) should never be due, got %v", got)
	}
}

func TestSchedulerRunsDueSyncsAndStops(t *testing.T) {
	env := newTestEnv(t, func(c *ManagerConfig) {
		c.InitialDelay = 10 * time.Millisecond
		c.TickInterval = 20 * time.Millisecond
	})
	mv := env.addMirror(t, MirrorInput{Repo: "o/r"})
	env.m.store.update(func(d *mirrorData) error {
		d.Mirrors[d.findMirror(mv.ID)].Enabled = true
		return nil
	})
	vdir := filepath.Join(env.folderDir("r"), "v1")
	env.gh.setJSON("/repos/o/r/releases/latest", testRelease("v1", "", "http://dl", testAsset{"a.bin", 3}))
	env.a2.setTrackAdded(true)

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		env.m.Run(ctx)
		close(stopped)
	}()
	waitFor(t, "scheduled sync", func() bool { return env.state(mv.ID).Status == StatusDownloading })
	// 模拟 aria2 完成下载，调度器的 RefreshProgress 应把状态推进到 synced
	writeTestFile(t, filepath.Join(vdir, "a.bin"), 3)
	waitFor(t, "scheduler progress refresh", func() bool { return env.state(mv.ID).Status == StatusSynced })
	time.Sleep(100 * time.Millisecond)
	if n := env.gh.requestCount(); n != 1 {
		t.Errorf("mirror re-synced before interval elapsed: %d requests", n)
	}
	cancel()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("scheduler did not stop on context cancel")
	}

	// 初始延迟期间取消也能立即退出
	env2 := newTestEnv(t, func(c *ManagerConfig) { c.InitialDelay = time.Hour })
	ctx2, cancel2 := context.WithCancel(context.Background())
	stopped2 := make(chan struct{})
	go func() {
		env2.m.Run(ctx2)
		close(stopped2)
	}()
	cancel2()
	select {
	case <-stopped2:
	case <-time.After(2 * time.Second):
		t.Fatal("scheduler did not stop during initial delay")
	}
}

func TestSyncAllAsyncOnlyEnabled(t *testing.T) {
	env := newTestEnv(t, nil)
	a := env.addMirror(t, MirrorInput{Repo: "o/a"})
	env.addMirror(t, MirrorInput{Repo: "o/b"})
	env.m.store.update(func(d *mirrorData) error {
		d.Mirrors[d.findMirror(a.ID)].Enabled = true
		return nil
	})
	env.m.SyncAllAsync()
	env.m.Wait()
	if env.gh.requestsFor("/repos/o/a/releases/latest") != 1 || env.gh.requestsFor("/repos/o/b/releases/latest") != 0 {
		t.Errorf("SyncAll should sync only enabled mirrors, requests: %d", env.gh.requestCount())
	}
}

func TestConcurrentOperationsRace(t *testing.T) {
	env := newTestEnv(t, nil)
	var ids []string
	for i := 0; i < 3; i++ {
		repo := "o/r" + strconv.Itoa(i)
		mv := env.addMirror(t, MirrorInput{Repo: repo})
		ids = append(ids, mv.ID)
		env.gh.setJSON("/repos/"+repo+"/releases/latest", testRelease("v1", "notes", "http://dl", testAsset{"a.bin", 2}))
	}
	env.a2.setTrackAdded(true)
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 15; i++ {
				id := ids[(w+i)%len(ids)]
				switch (w + i) % 5 {
				case 0:
					env.m.Sync(context.Background(), id)
				case 1:
					env.m.RefreshProgress(context.Background())
				case 2:
					env.m.ListMirrors()
				case 3:
					env.m.UpdateSettings(SettingsInput{Proxy: strPtr("")})
				case 4:
					env.m.SyncAsync(id)
					env.m.IsManagedPath(filepath.Join(env.m.downloadDir, "github-mirror", "x"))
				}
			}
		}(w)
	}
	wg.Wait()
	env.m.Wait()
	for _, id := range ids {
		if st := env.state(id); st.Status == StatusChecking || st.Status == StatusError {
			t.Errorf("mirror %s status = %s (%s)", id, st.Status, st.LastError)
		}
	}
}

// releaseWithURLs 构造资产下载地址任意指定的 Release（name, url 成对出现）
func releaseWithURLs(tag string, size int64, nameURLs ...string) map[string]interface{} {
	rel := testRelease(tag, "", "")
	list := []map[string]interface{}{}
	for i := 0; i+1 < len(nameURLs); i += 2 {
		list = append(list, map[string]interface{}{"name": nameURLs[i], "size": size, "browser_download_url": nameURLs[i+1]})
	}
	rel["assets"] = list
	return rel
}

func TestCheckAssetURL(t *testing.T) {
	for _, api := range []string{"https://api.github.com", "https://api.github.com/", "HTTPS://API.GITHUB.COM"} {
		env := newTestEnv(t, func(c *ManagerConfig) { c.GitHubAPI = api })
		if !env.m.githubHostOnly {
			t.Errorf("GitHub API %q should restrict asset hosts to github.com", api)
		}
	}
	env := newTestEnv(t, nil)
	if env.m.githubHostOnly {
		t.Error("custom GitHub API must not restrict asset hosts")
	}
	cases := []struct {
		url      string
		hostOnly bool
		ok       bool
	}{
		{"https://github.com/o/r/releases/download/v1/a.exe", true, true},
		{"https://GitHub.com/o/r/releases/download/v1/a.exe", true, true},
		{"http://github.com/o/r/releases/download/v1/a.exe", true, true},
		{"https://objects.githubusercontent.com/a.exe", true, false},
		{"https://github.com.evil.example/a.exe", true, false},
		{"https://github.com:8443/a.exe", true, false},
		{"http://127.0.0.1:6800/jsonrpc", true, false},
		{"http://169.254.169.254/latest/meta-data", true, false},
		{"http://127.0.0.1:8080/download/fake-api/a.exe", false, true},
		{"https://mirror.example/a.exe", false, true},
		{"file:///etc/passwd", false, false},
		{"ftp://example.com/a.exe", false, false},
		{"//github.com/a.exe", false, false},
		{"http:github.com", false, false},
		{"", false, false},
	}
	for _, c := range cases {
		env.m.githubHostOnly = c.hostOnly
		if err := env.m.checkAssetURL(c.url); (err == nil) != c.ok {
			t.Errorf("checkAssetURL(%q, hostOnly=%v) = %v, want ok=%v", c.url, c.hostOnly, err, c.ok)
		}
	}
}

func TestSyncSkipsUntrustedAssetURLs(t *testing.T) {
	env := newTestEnv(t, nil)
	env.m.githubHostOnly = true // 等同于使用默认的 https://api.github.com
	mv := env.addMirror(t, MirrorInput{Repo: "o/r", SaveChangelog: boolPtr(false)})
	vdir := filepath.Join(env.folderDir("r"), "v1")
	okURL := "https://github.com/o/r/releases/download/v1/ok.exe"
	env.gh.setJSON("/repos/o/r/releases/latest", releaseWithURLs("v1", 3,
		"ok.exe", okURL,
		"internal.bin", "http://169.254.169.254/latest/meta-data",
		"local.bin", "file:///etc/passwd",
		"done.bin", "http://10.0.0.1/done.bin", // 已完整存在，不需要下载
	))
	writeTestFile(t, filepath.Join(vdir, "done.bin"), 3)

	if err := env.m.Sync(context.Background(), mv.ID); err != nil {
		t.Fatal(err)
	}
	calls := env.a2.calls()
	if len(calls) != 1 || !reflect.DeepEqual(calls[0].uris, []string{okURL}) {
		t.Fatalf("only the github.com asset may be queued, got %+v", calls)
	}
	st := env.state(mv.ID)
	if st.Status != StatusDownloading || st.LastError != "" {
		t.Errorf("state = %+v", st)
	}
	files := map[string]MirrorFile{}
	for _, f := range st.Files {
		files[f.Name] = f
	}
	if f := files["ok.exe"]; f.Status != FileDownloading || f.Error != "" {
		t.Errorf("ok.exe = %+v", f)
	}
	if f := files["internal.bin"]; f.Status != FilePending || !strings.Contains(f.Error, "169.254.169.254") || !strings.Contains(f.Error, "github.com") {
		t.Errorf("internal.bin = %+v", f)
	}
	if f := files["local.bin"]; f.Status != FilePending || !strings.Contains(f.Error, "http/https") {
		t.Errorf("local.bin = %+v", f)
	}
	if f := files["done.bin"]; f.Status != FileDone || f.Error != "" {
		t.Errorf("done.bin = %+v", f)
	}

	// 合法资产下载完成 → incomplete，lastError 汇总被拒绝的原因
	writeTestFile(t, filepath.Join(vdir, "ok.exe"), 3)
	if err := env.m.RefreshProgress(context.Background()); err != nil {
		t.Fatal(err)
	}
	st = env.state(mv.ID)
	want := "部分文件下载失败：internal.bin：" + files["internal.bin"].Error + "；local.bin：" + files["local.bin"].Error
	if st.Status != StatusIncomplete || st.LastError != want {
		t.Errorf("state = %s / %q, want incomplete / %q", st.Status, st.LastError, want)
	}

	// 再次同步：仍不投递，直接得出 incomplete
	if err := env.m.Sync(context.Background(), mv.ID); err != nil {
		t.Fatal(err)
	}
	if n := len(env.a2.calls()); n != 1 {
		t.Errorf("untrusted assets queued on re-sync: %d addUri calls", n)
	}
	if st = env.state(mv.ID); st.Status != StatusIncomplete || st.LastError != want {
		t.Errorf("re-sync state = %s / %q", st.Status, st.LastError)
	}
}

func TestSyncSkipsGeneratedChangelogWhenAssetExists(t *testing.T) {
	for _, assetName := range []string{"CHANGELOG.md", "changelog.MD"} {
		env := newTestEnv(t, nil)
		mv := env.addMirror(t, MirrorInput{Repo: "o/r"})
		env.gh.setJSON("/repos/o/r/releases/latest", testRelease("v1", "release notes", "http://dl",
			testAsset{assetName, 100}, testAsset{"a.bin", 1}))
		if err := env.m.Sync(context.Background(), mv.ID); err != nil {
			t.Fatal(err)
		}
		vdir := filepath.Join(env.folderDir("r"), "v1")
		if _, err := os.Stat(filepath.Join(vdir, "CHANGELOG.md")); !os.IsNotExist(err) {
			t.Errorf("generated CHANGELOG.md written although asset %q exists (err=%v)", assetName, err)
		}
		queued := false
		for _, c := range env.a2.calls() {
			if c.options["out"] == assetName {
				queued = true
			}
		}
		if !queued {
			t.Errorf("asset %q not queued: %+v", assetName, env.a2.calls())
		}
	}

	// CHANGELOG.md 资产被过滤掉时，仍写出 Release 说明
	env := newTestEnv(t, nil)
	mv := env.addMirror(t, MirrorInput{Repo: "o/r", FileTypes: flexStrings{"bin"}})
	env.gh.setJSON("/repos/o/r/releases/latest", testRelease("v1", "release notes", "http://dl",
		testAsset{"CHANGELOG.md", 100}, testAsset{"a.bin", 1}))
	if err := env.m.Sync(context.Background(), mv.ID); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(env.folderDir("r"), "v1", "CHANGELOG.md")); err != nil || string(b) != "release notes" {
		t.Errorf("CHANGELOG = %q, %v", b, err)
	}
	if calls := env.a2.calls(); len(calls) != 1 || calls[0].options["out"] != "a.bin" {
		t.Errorf("addUri calls = %+v", calls)
	}
}

func TestDeleteMirrorDuringBlockedSync(t *testing.T) {
	for _, deleteFiles := range []bool{false, true} {
		t.Run(fmt.Sprintf("deleteFiles=%v", deleteFiles), func(t *testing.T) {
			env := newTestEnv(t, nil)
			mv := env.addMirror(t, MirrorInput{Repo: "o/r", Folder: "App", KeepVersions: 1})
			folder := env.folderDir("App")
			oldFile := filepath.Join(folder, "v0", "old.bin")
			writeTestFile(t, oldFile, 1)
			env.gh.setJSON("/repos/o/r/releases/latest", testRelease("v1", "notes", "http://dl", testAsset{"a.bin", 1}))
			block := make(chan struct{})
			env.gh.setBlock(block)
			defer close(block)

			syncErr := make(chan error, 1)
			go func() { syncErr <- env.m.Sync(context.Background(), mv.ID) }()
			waitFor(t, "GitHub request", func() bool { return env.gh.requestCount() == 1 })

			deleted := make(chan error, 1)
			go func() { deleted <- env.m.DeleteMirror(mv.ID, deleteFiles) }()
			select {
			case err := <-deleted:
				if err != nil {
					t.Fatalf("DeleteMirror: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("DeleteMirror did not cancel the blocked sync")
			}
			// DeleteMirror 返回前，正在进行的同步已经结束
			select {
			case err := <-syncErr:
				if err == nil {
					t.Error("cancelled sync should return an error")
				}
			default:
				t.Fatal("DeleteMirror returned before the running sync finished")
			}

			if env.m.HasMirror(mv.ID) {
				t.Error("mirror still exists")
			}
			env.m.store.view(func(d *mirrorData) {
				if _, ok := d.State[mv.ID]; ok {
					t.Error("state re-created for deleted mirror")
				}
			})
			if n := len(env.a2.calls()); n != 0 {
				t.Errorf("deleted mirror queued %d downloads", n)
			}
			if _, err := os.Stat(filepath.Join(folder, "v1")); !os.IsNotExist(err) {
				t.Error("version dir re-created for deleted mirror")
			}
			_, err := os.Stat(oldFile)
			if deleteFiles {
				if _, ferr := os.Stat(folder); !os.IsNotExist(ferr) {
					t.Error("mirror folder not removed")
				}
			} else if err != nil {
				t.Error("old version must be untouched (no retention for a deleted mirror)")
			}
		})
	}
}

func TestSyncAbortsWhenMirrorRemovedMidway(t *testing.T) {
	env := newTestEnv(t, nil)
	mv := env.addMirror(t, MirrorInput{Repo: "o/r", Folder: "App", KeepVersions: 1})
	folder := env.folderDir("App")
	oldFile := filepath.Join(folder, "v0", "old.bin")
	writeTestFile(t, oldFile, 1)
	env.gh.setJSON("/repos/o/r/releases/latest", testRelease("v1", "notes", "http://dl", testAsset{"a.bin", 1}))
	block := make(chan struct{})
	env.gh.setBlock(block)
	done := make(chan error, 1)
	go func() { done <- env.m.Sync(context.Background(), mv.ID) }()
	waitFor(t, "GitHub request", func() bool { return env.gh.requestCount() == 1 })

	// 不经过 DeleteMirror（不取消同步），只移除配置：验证写盘/投递前在 store 锁内的二次确认
	env.m.store.update(func(d *mirrorData) error {
		i := d.findMirror(mv.ID)
		d.Mirrors = append(d.Mirrors[:i], d.Mirrors[i+1:]...)
		delete(d.State, mv.ID)
		return nil
	})
	env.gh.setBlock(nil)
	close(block)
	if err := <-done; !errors.Is(err, errMirrorNotFound) {
		t.Errorf("Sync err = %v, want errMirrorNotFound", err)
	}
	if n := len(env.a2.calls()); n != 0 {
		t.Errorf("removed mirror queued %d downloads", n)
	}
	if _, err := os.Stat(filepath.Join(folder, "v1")); !os.IsNotExist(err) {
		t.Error("version dir / CHANGELOG created for removed mirror")
	}
	if _, err := os.Stat(oldFile); err != nil {
		t.Error("retention must not run for a removed mirror")
	}
	env.m.store.view(func(d *mirrorData) {
		if _, ok := d.State[mv.ID]; ok {
			t.Error("state re-created for removed mirror")
		}
	})
}

func TestDeleteMirrorRemovesAria2Tasks(t *testing.T) {
	env := newTestEnv(t, nil)
	mv := env.addMirror(t, MirrorInput{Repo: "o/r", Folder: "App"})
	folder := env.folderDir("App")
	env.a2.setActive(
		fakeTask{gid: "in-active", path: filepath.Join(folder, "v1", "a.bin")},
		fakeTask{gid: "other-folder", path: filepath.Join(env.folderDir("AppX"), "v1", "a.bin")},
	)
	env.a2.setWaiting(
		fakeTask{gid: "in-paused", path: filepath.Join(folder, "v0", "b.bin")},
		fakeTask{gid: "outside", path: filepath.Join(env.m.downloadDir, "movie.mkv")},
	)
	writeTestFile(t, filepath.Join(folder, "v1", "a.bin"), 1)

	if err := env.m.DeleteMirror(mv.ID, false); err != nil {
		t.Fatal(err)
	}
	removed := env.a2.removedGIDs()
	sort.Strings(removed)
	if !reflect.DeepEqual(removed, []string{"in-active", "in-paused"}) {
		t.Errorf("forceRemove gids = %v, want only tasks inside the mirror folder", removed)
	}
	if _, err := os.Stat(filepath.Join(folder, "v1", "a.bin")); err != nil {
		t.Error("files must be kept without deleteFiles=true")
	}

	// aria2 不可用时删除仍然成功（尽力而为）
	mv2 := env.addMirror(t, MirrorInput{Repo: "o/r2", Folder: "App2"})
	writeTestFile(t, filepath.Join(env.folderDir("App2"), "v1", "a.bin"), 1)
	env.a2.srv.Close()
	if err := env.m.DeleteMirror(mv2.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(env.folderDir("App2")); !os.IsNotExist(err) {
		t.Error("mirror folder not removed")
	}
}

func TestRetentionWaitsUntilSynced(t *testing.T) {
	env := newTestEnv(t, nil)
	mv := env.addMirror(t, MirrorInput{Repo: "o/r", Folder: "App", KeepVersions: 1, SaveChangelog: boolPtr(false)})
	folder := env.folderDir("App")
	oldDir := filepath.Join(folder, "v1")
	writeTestFile(t, filepath.Join(oldDir, "a.bin"), 1)
	env.gh.setJSON("/repos/o/r/releases/latest", testRelease("v2", "", "http://dl", testAsset{"a.bin", 4}))
	env.a2.setTrackAdded(true)
	exists := func(p string) bool {
		_, err := os.Stat(p)
		return err == nil
	}

	if err := env.m.Sync(context.Background(), mv.ID); err != nil {
		t.Fatal(err)
	}
	if st := env.state(mv.ID); st.Status != StatusDownloading {
		t.Fatalf("status = %s", st.Status)
	}
	if !exists(oldDir) {
		t.Fatal("old version removed while the new version is still downloading")
	}
	if err := env.m.RefreshProgress(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !exists(oldDir) {
		t.Fatal("old version removed by a refresh while still downloading")
	}

	// 新版本下载完成 → RefreshProgress 推进到 synced 后执行保留
	writeTestFile(t, filepath.Join(folder, "v2", "a.bin"), 4)
	env.a2.setWaiting()
	if err := env.m.RefreshProgress(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := env.state(mv.ID); st.Status != StatusSynced {
		t.Fatalf("status = %s, want synced", st.Status)
	}
	if exists(oldDir) || !exists(filepath.Join(folder, "v2", "a.bin")) {
		t.Error("retention should remove the old version once the current one is synced")
	}
}

func TestRefreshProgressReportsAria2Errors(t *testing.T) {
	env := newTestEnv(t, nil)
	mv := env.addMirror(t, MirrorInput{Repo: "o/r", SaveChangelog: boolPtr(false)})
	other := env.addMirror(t, MirrorInput{Repo: "o/other", SaveChangelog: boolPtr(false)})
	env.gh.setJSON("/repos/o/r/releases/latest", testRelease("v1", "", "http://dl",
		testAsset{"a.bin", 2}, testAsset{"b.bin", 2}, testAsset{"c.bin", 2}, testAsset{"d.bin", 2}))
	env.gh.setJSON("/repos/o/other/releases/latest", testRelease("v9", "", "http://dl", testAsset{"x.bin", 2}))
	for _, id := range []string{mv.ID, other.ID} {
		if err := env.m.Sync(context.Background(), id); err != nil {
			t.Fatal(err)
		}
	}
	vdir := filepath.Join(env.folderDir("r"), "v1")
	odir := filepath.Join(env.folderDir("other"), "v9")
	// tellStopped 按从旧到新排列：同一路径以最后一条为准
	env.a2.setStopped(
		fakeStoppedTask{gid: "s1", status: "error", path: filepath.Join(vdir, "a.bin"), errorCode: "3", errorMessage: "Resource not found"},
		fakeStoppedTask{gid: "s2", status: "error", path: filepath.Join(vdir, "b.bin"), errorCode: "1", errorMessage: "old failure"},
		fakeStoppedTask{gid: "s3", status: "removed", path: filepath.Join(vdir, "b.bin")},
		fakeStoppedTask{gid: "s4", status: "error", path: filepath.Join(vdir, "a.bin"), errorCode: "22", errorMessage: "HTTP 503"},
		fakeStoppedTask{gid: "s5", status: "error", path: filepath.Join(vdir, "c.bin"), errorCode: "1", errorMessage: "network down"},
		fakeStoppedTask{gid: "s6", status: "error", path: filepath.Join(odir, "x.bin"), errorCode: "9"},
		fakeStoppedTask{gid: "s7", status: "complete", path: filepath.Join(vdir, "d.bin")},
	)
	writeTestFile(t, filepath.Join(vdir, "d.bin"), 2)
	env.a2.setActive(fakeTask{path: filepath.Join(vdir, "c.bin"), completed: 1, total: 2})

	// r 仍有文件在下载（保持 downloading）；other 变为 incomplete 并补全原因
	if err := env.m.RefreshProgress(context.Background()); err != nil {
		t.Fatal(err)
	}
	st := env.state(mv.ID)
	if st.Status != StatusDownloading || st.LastError != "" {
		t.Errorf("r state = %s / %q", st.Status, st.LastError)
	}
	ost := env.state(other.ID)
	if ost.Status != StatusIncomplete || ost.Files[0].Error != "错误码 9" || ost.LastError != "部分文件下载失败：x.bin：错误码 9" {
		t.Errorf("other state = %+v", ost)
	}
	calls := env.a2.stoppedCalls()
	if len(calls) != 1 {
		t.Fatalf("tellStopped calls = %d, want 1", len(calls))
	}
	if p := calls[0]; len(p) != 3 || string(p[0]) != "0" || string(p[1]) != "1000" ||
		string(p[2]) != `["gid","status","files","errorCode","errorMessage"]` {
		t.Errorf("tellStopped params = %s", p)
	}

	// c.bin 停止 → incomplete
	env.a2.setActive()
	if err := env.m.RefreshProgress(context.Background()); err != nil {
		t.Fatal(err)
	}
	st = env.state(mv.ID)
	wantErr := map[string]string{"a.bin": "HTTP 503", "b.bin": "", "c.bin": "network down", "d.bin": ""}
	for _, f := range st.Files {
		if f.Error != wantErr[f.Name] {
			t.Errorf("%s error = %q, want %q", f.Name, f.Error, wantErr[f.Name])
		}
	}
	if st.Status != StatusIncomplete || st.LastError != "部分文件下载失败：a.bin：HTTP 503；c.bin：network down" {
		t.Errorf("r state = %s / %q", st.Status, st.LastError)
	}
	if n := len(env.a2.stoppedCalls()); n != 2 {
		t.Errorf("tellStopped calls = %d, want 2", n)
	}
	s2, err := OpenMirrorStore(env.m.cfg.ConfigDir)
	if err != nil {
		t.Fatal(err)
	}
	s2.view(func(d *mirrorData) {
		if f := d.State[mv.ID].Files[0]; f.Name != "a.bin" || f.Error != "HTTP 503" {
			t.Errorf("file error not persisted: %+v", f)
		}
	})

	// 重新同步：pending 文件重新投递，error 清空
	if err := env.m.Sync(context.Background(), mv.ID); err != nil {
		t.Fatal(err)
	}
	st = env.state(mv.ID)
	if st.Status != StatusDownloading || st.LastError != "" {
		t.Errorf("state after re-sync = %s / %q", st.Status, st.LastError)
	}
	for _, f := range st.Files {
		if f.Error != "" {
			t.Errorf("%s error not cleared: %q", f.Name, f.Error)
		}
	}
}
