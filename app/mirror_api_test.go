package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type apiEnv struct {
	*testEnv
	mux *http.ServeMux
}

func newAPIEnv(t *testing.T) *apiEnv {
	env := newTestEnv(t, nil)
	mux := http.NewServeMux()
	RegisterMirrorAPI(mux, env.m)
	return &apiEnv{testEnv: env, mux: mux}
}

func (a *apiEnv) do(t *testing.T, method, path, body, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body != "" || method == http.MethodPost || method == http.MethodPut {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	a.mux.ServeHTTP(rec, req)
	return rec
}

func (a *apiEnv) doJSON(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	return a.do(t, method, path, body, "application/json")
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder, v interface{}) {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("invalid JSON response %q: %v", rec.Body.String(), err)
	}
}

func expectStatus(t *testing.T, rec *httptest.ResponseRecorder, code int) {
	t.Helper()
	if rec.Code != code {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, code, rec.Body.String())
	}
}

func expectError(t *testing.T, rec *httptest.ResponseRecorder, code int) string {
	t.Helper()
	expectStatus(t, rec, code)
	var body struct {
		Error string `json:"error"`
	}
	decodeBody(t, rec, &body)
	if body.Error == "" {
		t.Errorf("error response without message: %s", rec.Body.String())
	}
	return body.Error
}

// mirrorJSON 构造前端风格的完整 MirrorInput（禁用，避免触发同步）
func mirrorJSON(name, repo, folder string) string {
	b, _ := json.Marshal(map[string]interface{}{
		"name": name, "repo": repo, "folder": folder, "enabled": false, "saveChangelog": true,
		"fileTypes": []string{}, "regex": "", "includePrerelease": false, "keepVersions": 0,
	})
	return string(b)
}

func TestAPIListEmpty(t *testing.T) {
	a := newAPIEnv(t)
	rec := a.do(t, http.MethodGet, "/api/mirrors", "", "")
	expectStatus(t, rec, http.StatusOK)
	var body map[string]json.RawMessage
	decodeBody(t, rec, &body)
	if string(body["mirrors"]) != "[]" {
		t.Errorf("mirrors = %s, want []", body["mirrors"])
	}
	var dir string
	json.Unmarshal(body["downloadDir"], &dir)
	if dir != a.m.downloadDir {
		t.Errorf("downloadDir = %q", dir)
	}
	var sv SettingsView
	json.Unmarshal(body["settings"], &sv)
	if sv != (SettingsView{RootDir: "github-mirror", IntervalMinutes: 360}) {
		t.Errorf("settings = %+v", sv)
	}
	if !strings.Contains(string(body["settings"]), `"githubTokenSet":false`) {
		t.Errorf("settings JSON = %s", body["settings"])
	}
}

func TestAPICreateValidation(t *testing.T) {
	a := newAPIEnv(t)
	cases := []struct {
		name, body, ct string
		code           int
	}{
		{"text/plain", `{"repo":"a/b"}`, "text/plain", 415},
		{"no content type", `{"repo":"a/b"}`, "", 415},
		{"form", `repo=a/b`, "application/x-www-form-urlencoded", 415},
		{"invalid repo", mirrorJSON("bad", "not a valid repo!", ""), "application/json", 400},
		{"invalid regex", `{"repo":"a/b","regex":"("}`, "application/json", 400},
		{"folder dotdot", `{"repo":"a/b","folder":".."}`, "application/json", 400},
		{"folder slash", `{"repo":"a/b","folder":"x/y"}`, "application/json", 400},
		{"negative keep", `{"repo":"a/b","keepVersions":-1}`, "application/json", 400},
		{"bad json", `{"repo":`, "application/json", 400},
		{"empty body", ``, "application/json", 400},
		{"wrong type", `{"repo":123}`, "application/json", 400},
		{"too large", `{"repo":"a/b","name":"` + strings.Repeat("x", maxAPIBodyBytes) + `"}`, "application/json", 413},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := a.do(t, http.MethodPost, "/api/mirrors", c.body, c.ct)
			expectError(t, rec, c.code)
		})
	}
	_, views := a.m.ListMirrors()
	if len(views) != 0 {
		t.Errorf("invalid requests created mirrors: %+v", views)
	}
	// 带 charset 的 JSON 允许
	rec := a.do(t, http.MethodPost, "/api/mirrors", `{"repo":"a/b","enabled":false}`, "application/json; charset=utf-8")
	expectStatus(t, rec, http.StatusCreated)
}

func TestAPICreateDefaultsAndTriggersSync(t *testing.T) {
	a := newAPIEnv(t)
	rec := a.doJSON(t, http.MethodPost, "/api/mirrors", `{"repo":"https://github.com/Owner/Repo/releases","unknownField":1}`)
	expectStatus(t, rec, http.StatusCreated)
	var raw map[string]interface{}
	json.Unmarshal(rec.Body.Bytes(), &raw)
	for _, k := range []string{"id", "name", "repo", "folder", "enabled", "saveChangelog", "fileTypes", "regex", "includePrerelease", "keepVersions", "createdAt", "state"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("MirrorView missing %q: %s", k, rec.Body.String())
		}
	}
	var v MirrorView
	decodeBody(t, rec, &v)
	if len(v.ID) != 16 || v.Name != "Repo" || v.Folder != "Repo" || v.Repo != "Owner/Repo" || !v.Enabled || !v.SaveChangelog {
		t.Errorf("created = %+v", v)
	}
	if v.FileTypes == nil || v.CreatedAt.IsZero() || v.State == nil || v.State.Status != StatusIdle || v.State.Files == nil {
		t.Errorf("created = %+v state=%+v", v, v.State)
	}
	st := raw["state"].(map[string]interface{})
	if st["lastCheck"] != "" || st["lastSuccess"] != "" || st["publishedAt"] != "" {
		t.Errorf("never-happened times must be empty strings: %v", st)
	}
	// enabled → 立即异步同步（mock 返回 404）
	a.m.Wait()
	if a.gh.requestsFor("/repos/Owner/Repo/releases/latest") != 1 {
		t.Errorf("create did not trigger sync")
	}
	if got := a.state(v.ID); got.Status != StatusError || got.LastError != "仓库不存在或没有正式 Release" {
		t.Errorf("state after sync = %+v", got)
	}

	// 文件夹重复（大小写不敏感）→ 400
	expectError(t, a.doJSON(t, http.MethodPost, "/api/mirrors", mirrorJSON("dup", "x/y", "repo")), 400)
	// 禁用的镜像不触发同步
	rec = a.doJSON(t, http.MethodPost, "/api/mirrors", mirrorJSON("off", "x/off", ""))
	expectStatus(t, rec, http.StatusCreated)
	a.m.Wait()
	if a.gh.requestsFor("/repos/x/off/releases/latest") != 0 {
		t.Error("disabled mirror must not be synced on create")
	}
}

func TestAPIUpdateMirror(t *testing.T) {
	a := newAPIEnv(t)
	v1 := a.addMirror(t, MirrorInput{Repo: "o/one", Folder: "One"})
	a.addMirror(t, MirrorInput{Repo: "o/two", Folder: "Two"})

	rec := a.doJSON(t, http.MethodPut, "/api/mirrors/"+v1.ID, `{"name":"Renamed","repo":"o/one","folder":"OneNew","enabled":false,"saveChangelog":false,"fileTypes":["zip"],"regex":"x","includePrerelease":true,"keepVersions":"3"}`)
	expectStatus(t, rec, http.StatusOK)
	var got MirrorView
	decodeBody(t, rec, &got)
	if got.ID != v1.ID || got.Name != "Renamed" || got.Folder != "OneNew" || got.SaveChangelog || !got.IncludePrerelease || got.KeepVersions != 3 ||
		got.Regex != "x" || len(got.FileTypes) != 1 || !got.CreatedAt.Equal(v1.CreatedAt.Time) {
		t.Errorf("updated = %+v", got)
	}
	a.m.Wait()
	if a.gh.requestCount() != 0 {
		t.Error("update without enabling must not sync")
	}

	expectError(t, a.doJSON(t, http.MethodPut, "/api/mirrors/nope", mirrorJSON("x", "o/x", "X")), 404)
	expectError(t, a.doJSON(t, http.MethodPut, "/api/mirrors/"+v1.ID, mirrorJSON("x", "o/one", "two")), 400)
	expectError(t, a.doJSON(t, http.MethodPut, "/api/mirrors/"+v1.ID, `{"repo":"o/one","regex":"["}`), 400)
	expectError(t, a.do(t, http.MethodPut, "/api/mirrors/"+v1.ID, `{"repo":"o/one"}`, "text/plain"), 415)

	// 从禁用变为启用 → 异步同步
	rec = a.doJSON(t, http.MethodPut, "/api/mirrors/"+v1.ID, `{"name":"Renamed","repo":"o/one","folder":"OneNew","enabled":true}`)
	expectStatus(t, rec, http.StatusOK)
	a.m.Wait()
	if a.gh.requestsFor("/repos/o/one/releases/latest") != 1 {
		t.Error("enabling a mirror must trigger a sync")
	}
	// 启用 → 启用：不再触发；enabled 缺省时保持原值
	rec = a.doJSON(t, http.MethodPut, "/api/mirrors/"+v1.ID, `{"repo":"o/one","folder":"OneNew"}`)
	expectStatus(t, rec, http.StatusOK)
	decodeBody(t, rec, &got)
	a.m.Wait()
	if !got.Enabled || a.gh.requestsFor("/repos/o/one/releases/latest") != 1 {
		t.Errorf("enabled=%v requests=%d", got.Enabled, a.gh.requestsFor("/repos/o/one/releases/latest"))
	}
}

func TestAPIDeleteMirror(t *testing.T) {
	a := newAPIEnv(t)
	keep := a.addMirror(t, MirrorInput{Repo: "o/keep", Folder: "Keep"})
	drop := a.addMirror(t, MirrorInput{Repo: "o/drop", Folder: "Drop"})
	keepFile := filepath.Join(a.folderDir("Keep"), "v1", "a.bin")
	dropFile := filepath.Join(a.folderDir("Drop"), "v1", "a.bin")
	writeTestFile(t, keepFile, 1)
	writeTestFile(t, dropFile, 1)

	expectError(t, a.do(t, http.MethodDelete, "/api/mirrors/unknown", "", ""), 404)

	rec := a.do(t, http.MethodDelete, "/api/mirrors/"+keep.ID, "", "")
	expectStatus(t, rec, http.StatusOK)
	if strings.TrimSpace(rec.Body.String()) != `{"ok":true}` {
		t.Errorf("body = %s", rec.Body.String())
	}
	if _, err := os.Stat(keepFile); err != nil {
		t.Error("files must be kept without deleteFiles=true")
	}

	rec = a.do(t, http.MethodDelete, "/api/mirrors/"+drop.ID+"?deleteFiles=true", "", "")
	expectStatus(t, rec, http.StatusOK)
	if _, err := os.Stat(a.folderDir("Drop")); !os.IsNotExist(err) {
		t.Error("mirror folder not removed with deleteFiles=true")
	}
	if _, err := os.Stat(keepFile); err != nil {
		t.Error("other mirror folders must not be touched")
	}
	if _, err := os.Stat(filepath.Join(a.m.downloadDir, "github-mirror")); err != nil {
		t.Error("root dir must not be removed")
	}
	_, views := a.m.ListMirrors()
	if len(views) != 0 {
		t.Errorf("mirrors left: %+v", views)
	}
	a.m.store.view(func(d *mirrorData) {
		if len(d.State) != 0 {
			t.Errorf("state not cleaned: %+v", d.State)
		}
	})
	expectError(t, a.do(t, http.MethodDelete, "/api/mirrors/"+drop.ID, "", ""), 404)

	// 文件夹不存在时 deleteFiles=true 也成功
	gone := a.addMirror(t, MirrorInput{Repo: "o/gone"})
	expectStatus(t, a.do(t, http.MethodDelete, "/api/mirrors/"+gone.ID+"?deleteFiles=true", "", ""), http.StatusOK)
}

func TestAPISyncEndpoints(t *testing.T) {
	a := newAPIEnv(t)
	on := a.addMirror(t, MirrorInput{Repo: "o/on", Enabled: boolPtr(false)})
	a.addMirror(t, MirrorInput{Repo: "o/off"})
	a.m.store.update(func(d *mirrorData) error { d.Mirrors[d.findMirror(on.ID)].Enabled = true; return nil })

	rec := a.doJSON(t, http.MethodPost, "/api/mirrors/"+on.ID+"/sync", `{}`)
	expectStatus(t, rec, http.StatusAccepted)
	if strings.TrimSpace(rec.Body.String()) != `{"ok":true}` {
		t.Errorf("body = %s", rec.Body.String())
	}
	a.m.Wait()
	if a.gh.requestsFor("/repos/o/on/releases/latest") != 1 {
		t.Error("manual sync not executed")
	}
	expectError(t, a.doJSON(t, http.MethodPost, "/api/mirrors/unknown/sync", `{}`), 404)
	expectError(t, a.do(t, http.MethodPost, "/api/mirrors/"+on.ID+"/sync", `{}`, "text/plain"), 415)
	expectStatus(t, a.doJSON(t, http.MethodPost, "/api/mirrors/"+on.ID+"/sync", ``), http.StatusAccepted)
	a.m.Wait()

	before := a.gh.requestCount()
	expectError(t, a.do(t, http.MethodPost, "/api/mirrors/sync", `{}`, "text/plain"), 415)
	rec = a.doJSON(t, http.MethodPost, "/api/mirrors/sync", `{}`)
	expectStatus(t, rec, http.StatusAccepted)
	a.m.Wait()
	if a.gh.requestsFor("/repos/o/on/releases/latest") != 3 || a.gh.requestsFor("/repos/o/off/releases/latest") != 0 {
		t.Errorf("sync all: requests %d -> %d", before, a.gh.requestCount())
	}
}

func TestAPIVersions(t *testing.T) {
	a := newAPIEnv(t)
	v := a.addMirror(t, MirrorInput{Repo: "o/r", Folder: "My App"})
	folder := a.folderDir("My App")
	now := time.Now()
	writeTestFile(t, filepath.Join(folder, "v1", "old.bin"), 3)
	writeTestFile(t, filepath.Join(folder, "v2", "app setup#1.exe"), 5)
	writeTestFile(t, filepath.Join(folder, "v2", "中文 包.zip"), 2)
	writeTestFile(t, filepath.Join(folder, "v2", "CHANGELOG.md"), 4)
	writeTestFile(t, filepath.Join(folder, "v2", "partial.bin.aria2"), 1)
	writeTestFile(t, filepath.Join(folder, "v2", "nested", "deep.bin"), 1)
	writeTestFile(t, filepath.Join(folder, "stray.txt"), 1)
	os.Chtimes(filepath.Join(folder, "v1"), now.Add(-2*time.Hour), now.Add(-2*time.Hour))
	os.Chtimes(filepath.Join(folder, "v2"), now.Add(-time.Hour), now.Add(-time.Hour))

	rec := a.do(t, http.MethodGet, "/api/mirrors/"+v.ID+"/versions", "", "")
	expectStatus(t, rec, http.StatusOK)
	var body struct {
		Versions []VersionView `json:"versions"`
	}
	decodeBody(t, rec, &body)
	if len(body.Versions) != 2 || body.Versions[0].Tag != "v2" || body.Versions[1].Tag != "v1" {
		t.Fatalf("versions = %+v", body.Versions)
	}
	if body.Versions[0].ModTime.IsZero() {
		t.Error("modTime missing")
	}
	files := map[string]VersionFile{}
	for _, f := range body.Versions[0].Files {
		files[f.Name] = f
	}
	if len(files) != 3 {
		t.Errorf("files = %+v (must exclude .aria2 and directories)", body.Versions[0].Files)
	}
	want := map[string]string{
		"app setup#1.exe": "/download/github-mirror/My%20App/v2/app%20setup%231.exe",
		"中文 包.zip":        "/download/github-mirror/My%20App/v2/%E4%B8%AD%E6%96%87%20%E5%8C%85.zip",
		"CHANGELOG.md":    "/download/github-mirror/My%20App/v2/CHANGELOG.md",
	}
	for name, url := range want {
		if files[name].URL != url {
			t.Errorf("url[%s] = %q, want %q", name, files[name].URL, url)
		}
	}
	if files["app setup#1.exe"].Size != 5 {
		t.Errorf("size = %d", files["app setup#1.exe"].Size)
	}

	// 多级 rootDir
	a.m.UpdateSettings(SettingsInput{RootDir: strPtr("mirrors/github")})
	writeTestFile(t, filepath.Join(a.m.downloadDir, "mirrors", "github", "My App", "v9", "x.bin"), 1)
	rec = a.do(t, http.MethodGet, "/api/mirrors/"+v.ID+"/versions", "", "")
	decodeBody(t, rec, &body)
	if len(body.Versions) != 1 || body.Versions[0].Files[0].URL != "/download/mirrors/github/My%20App/v9/x.bin" {
		t.Errorf("multi-level rootDir versions = %+v", body.Versions)
	}

	// 目录不存在 → 空数组
	empty := a.addMirror(t, MirrorInput{Repo: "o/empty"})
	rec = a.do(t, http.MethodGet, "/api/mirrors/"+empty.ID+"/versions", "", "")
	expectStatus(t, rec, http.StatusOK)
	if strings.TrimSpace(rec.Body.String()) != `{"versions":[]}` {
		t.Errorf("empty versions body = %s", rec.Body.String())
	}
	expectError(t, a.do(t, http.MethodGet, "/api/mirrors/unknown/versions", "", ""), 404)
}

func TestAPIVersionsLatestAndPartial(t *testing.T) {
	a := newAPIEnv(t)
	v := a.addMirror(t, MirrorInput{Repo: "o/cli", Folder: "CLI"})
	folder := a.folderDir("CLI")
	latest := filepath.Join(folder, "cli_v1.4.0")
	old := filepath.Join(folder, "cli_v1.3.0")
	writeTestFile(t, filepath.Join(latest, "done.bin"), 2)
	writeTestFile(t, filepath.Join(latest, "loading.bin"), 2) // 预分配了完整大小，尚无控制文件
	writeTestFile(t, filepath.Join(latest, "ctl.bin"), 2)
	writeTestFile(t, filepath.Join(latest, "ctl.bin.aria2"), 1)
	writeTestFile(t, filepath.Join(latest, "CHANGELOG.md"), 1)
	writeTestFile(t, filepath.Join(old, "loading.bin"), 2) // 旧版本同名文件不受 state 影响
	writeTestFile(t, filepath.Join(old, "broken.bin"), 2)
	writeTestFile(t, filepath.Join(old, "broken.bin.aria2"), 1)
	now := time.Now()
	os.Chtimes(old, now.Add(-2*time.Hour), now.Add(-2*time.Hour))
	os.Chtimes(latest, now.Add(-time.Hour), now.Add(-time.Hour))
	a.m.store.updateState(func(d *mirrorData) (bool, error) {
		st := d.stateFor(v.ID)
		st.Status = StatusDownloading
		st.LatestTag = "cli/v1.4.0"
		st.Files = []MirrorFile{
			{Name: "done.bin", Size: 2, Status: FileDone, CompletedLength: 2},
			{Name: "loading.bin", Size: 2, Status: FileDownloading, CompletedLength: 1},
		}
		return false, nil
	})

	rec := a.do(t, http.MethodGet, "/api/mirrors/"+v.ID+"/versions", "", "")
	expectStatus(t, rec, http.StatusOK)
	raw := rec.Body.String()
	if strings.Count(raw, `"latest":true`) != 1 || strings.Contains(raw, `"latest":false`) || strings.Contains(raw, `"partial":false`) {
		t.Errorf("latest/partial must be omitted when false: %s", raw)
	}
	var body struct {
		Versions []VersionView `json:"versions"`
	}
	decodeBody(t, rec, &body)
	if len(body.Versions) != 2 || body.Versions[0].Tag != "cli_v1.4.0" || !body.Versions[0].Latest || body.Versions[1].Latest {
		t.Fatalf("versions = %+v", body.Versions)
	}
	check := func(v VersionView, want map[string]bool) {
		t.Helper()
		if len(v.Files) != len(want) {
			t.Errorf("%s files = %+v", v.Tag, v.Files)
		}
		for _, f := range v.Files {
			if p, ok := want[f.Name]; !ok || f.Partial != p {
				t.Errorf("%s/%s partial = %v, want %v", v.Tag, f.Name, f.Partial, p)
			}
		}
	}
	check(body.Versions[0], map[string]bool{"done.bin": false, "loading.bin": true, "ctl.bin": true, "CHANGELOG.md": false})
	check(body.Versions[1], map[string]bool{"loading.bin": false, "broken.bin": true})
}

func TestAPISettingsAndTokenMasking(t *testing.T) {
	a := newAPIEnv(t)
	const token = "ghp_SuperSecretValue123"
	readSettings := func(rec *httptest.ResponseRecorder) SettingsView {
		t.Helper()
		expectStatus(t, rec, http.StatusOK)
		if strings.Contains(rec.Body.String(), token) || strings.Contains(rec.Body.String(), `"githubToken"`) {
			t.Fatalf("token leaked: %s", rec.Body.String())
		}
		var sv SettingsView
		decodeBody(t, rec, &sv)
		return sv
	}

	sv := readSettings(a.do(t, http.MethodGet, "/api/mirror-settings", "", ""))
	if sv != (SettingsView{RootDir: "github-mirror", IntervalMinutes: 360}) {
		t.Errorf("default settings = %+v", sv)
	}

	sv = readSettings(a.doJSON(t, http.MethodPut, "/api/mirror-settings", `{"rootDir":"/mirrors/gh/","intervalMinutes":"720","proxy":"http://user:pass@proxy.lan:3128/","githubToken":" `+token+` "}`))
	if sv != (SettingsView{RootDir: "mirrors/gh", IntervalMinutes: 720, Proxy: "http://user:pass@proxy.lan:3128", GitHubTokenSet: true}) {
		t.Errorf("updated settings = %+v", sv)
	}
	a.m.store.view(func(d *mirrorData) {
		if d.Settings.GitHubToken != token {
			t.Errorf("stored token = %q", d.Settings.GitHubToken)
		}
	})
	cfgPath := filepath.Join(a.m.cfg.ConfigDir, mirrorStoreFile)
	if raw, _ := os.ReadFile(cfgPath); !strings.Contains(string(raw), token) {
		t.Error("token not persisted")
	}
	if fi, err := os.Stat(cfgPath); err != nil || (runtime.GOOS != "windows" && fi.Mode().Perm() != 0600) {
		t.Errorf("config file perm = %v, %v", fi.Mode().Perm(), err)
	}

	sv = readSettings(a.do(t, http.MethodGet, "/api/mirror-settings", "", ""))
	if !sv.GitHubTokenSet {
		t.Error("githubTokenSet should be true")
	}
	a.addMirror(t, MirrorInput{Repo: "o/r"})
	rec := a.do(t, http.MethodGet, "/api/mirrors", "", "")
	if strings.Contains(rec.Body.String(), token) || !strings.Contains(rec.Body.String(), `"githubTokenSet":true`) {
		t.Errorf("GET /api/mirrors token handling wrong: %s", rec.Body.String())
	}

	// githubToken 缺省 / null → 保持
	sv = readSettings(a.doJSON(t, http.MethodPut, "/api/mirror-settings", `{"rootDir":"mirrors/gh","intervalMinutes":360,"proxy":""}`))
	if !sv.GitHubTokenSet || sv.IntervalMinutes != 360 || sv.Proxy != "" {
		t.Errorf("omitted token: %+v", sv)
	}
	sv = readSettings(a.doJSON(t, http.MethodPut, "/api/mirror-settings", `{"intervalMinutes":0,"githubToken":null}`))
	if !sv.GitHubTokenSet || sv.IntervalMinutes != 0 || sv.RootDir != "mirrors/gh" {
		t.Errorf("null token: %+v", sv)
	}
	// "" → 清除
	sv = readSettings(a.doJSON(t, http.MethodPut, "/api/mirror-settings", `{"githubToken":""}`))
	if sv.GitHubTokenSet {
		t.Error("empty githubToken should clear the token")
	}

	bad := []string{
		`{"intervalMinutes":5}`,
		`{"intervalMinutes":43201}`,
		`{"intervalMinutes":-1}`,
		`{"intervalMinutes":"abc"}`,
		`{"proxy":"socks5://127.0.0.1:1080"}`,
		`{"proxy":"https://127.0.0.1:1080"}`,
		`{"rootDir":"../escape"}`,
		`{"rootDir":""}`,
		`{"rootDir":"a/../../b"}`,
		`{"githubToken":"two words"}`,
	}
	for _, b := range bad {
		expectError(t, a.doJSON(t, http.MethodPut, "/api/mirror-settings", b), 400)
	}
	expectError(t, a.do(t, http.MethodPut, "/api/mirror-settings", `{"intervalMinutes":60}`, "text/plain"), 415)
	sv = readSettings(a.do(t, http.MethodGet, "/api/mirror-settings", "", ""))
	if sv.IntervalMinutes != 0 || sv.RootDir != "mirrors/gh" {
		t.Errorf("invalid updates must not change settings: %+v", sv)
	}
}

func TestAPIListRefreshesProgress(t *testing.T) {
	a := newAPIEnv(t)
	v := a.addMirror(t, MirrorInput{Repo: "o/r"})
	a.gh.setJSON("/repos/o/r/releases/latest", testRelease("v1", "", "http://dl", testAsset{"a.bin", 2}))
	a.a2.setTrackAdded(true)
	if err := a.m.Sync(context.Background(), v.ID); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(a.folderDir("r"), "v1", "a.bin"), 2)
	rec := a.do(t, http.MethodGet, "/api/mirrors", "", "")
	expectStatus(t, rec, http.StatusOK)
	var body struct {
		Mirrors []MirrorView `json:"mirrors"`
	}
	decodeBody(t, rec, &body)
	if len(body.Mirrors) != 1 || body.Mirrors[0].State.Status != StatusSynced || body.Mirrors[0].State.Files[0].Status != FileDone {
		t.Errorf("GET did not refresh progress: %s", rec.Body.String())
	}
	// aria2 不可用时 GET 仍然成功
	a.a2.srv.Close()
	expectStatus(t, a.do(t, http.MethodGet, "/api/mirrors", "", ""), http.StatusOK)
}

func TestAPIImport(t *testing.T) {
	a := newAPIEnv(t)
	payload, _ := json.Marshal(map[string]string{"yaml": sampleScriptConfig})
	rec := a.doJSON(t, http.MethodPost, "/api/mirrors/import", string(payload))
	expectStatus(t, rec, http.StatusOK)
	var res ImportResult
	decodeBody(t, rec, &res)
	if res.Imported != 11 || len(res.Skipped) != 0 || len(res.Mirrors) != 11 {
		t.Errorf("import = %+v", res)
	}
	if !strings.Contains(rec.Body.String(), `"skipped":[]`) {
		t.Errorf("skipped must be an empty array: %s", rec.Body.String())
	}
	a.m.Wait()
	if a.gh.requestCount() != 0 {
		t.Error("import must not trigger sync")
	}
	rec = a.doJSON(t, http.MethodPost, "/api/mirrors/import", string(payload))
	decodeBody(t, rec, &res)
	if res.Imported != 0 || len(res.Skipped) != 11 || res.Skipped[0].Key != "opentrace" || res.Skipped[0].Reason == "" {
		t.Errorf("re-import = %+v", res)
	}
	expectError(t, a.doJSON(t, http.MethodPost, "/api/mirrors/import", `{"yaml":"track-list: [x"}`), 400)
	expectError(t, a.doJSON(t, http.MethodPost, "/api/mirrors/import", `{}`), 400)
	expectError(t, a.do(t, http.MethodPost, "/api/mirrors/import", string(payload), "text/yaml"), 415)
}

func TestAPIUnknownRoutes(t *testing.T) {
	a := newAPIEnv(t)
	expectError(t, a.do(t, http.MethodGet, "/api/unknown", "", ""), 404)
	expectError(t, a.do(t, http.MethodPatch, "/api/mirrors", "", ""), 404)
}
