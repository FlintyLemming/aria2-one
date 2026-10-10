package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const maxAPIBodyBytes = 1 << 20

// SettingsView 设置的对外视图：永远不包含 Token 明文
type SettingsView struct {
	RootDir         string `json:"rootDir"`
	IntervalMinutes int    `json:"intervalMinutes"`
	Proxy           string `json:"proxy"`
	GitHubTokenSet  bool   `json:"githubTokenSet"`
}

// SettingsInput 为 PUT /api/mirror-settings 的请求体；字段缺省或 null 时保持不变，githubToken 为 "" 表示清除
type SettingsInput struct {
	RootDir         *string  `json:"rootDir"`
	IntervalMinutes *flexInt `json:"intervalMinutes"`
	Proxy           *string  `json:"proxy"`
	GitHubToken     *string  `json:"githubToken"`
}

// MirrorView = Mirror 全部字段 + state
type MirrorView struct {
	Mirror
	State *MirrorState `json:"state"`
}

type VersionFile struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	URL  string `json:"url"`
	// Partial 存在 .aria2 控制文件，或为最新版本且状态中该文件未完成
	Partial bool `json:"partial,omitempty"`
}

type VersionView struct {
	Tag     string    `json:"tag"`
	ModTime Timestamp `json:"modTime"`
	// Latest 目录名等于安全化后的 state.latestTag
	Latest bool          `json:"latest,omitempty"`
	Files  []VersionFile `json:"files"`
}

type mirrorListResponse struct {
	Settings    SettingsView `json:"settings"`
	DownloadDir string       `json:"downloadDir"`
	Mirrors     []MirrorView `json:"mirrors"`
}

func settingsView(s MirrorSettings) SettingsView {
	return SettingsView{
		RootDir:         s.RootDir,
		IntervalMinutes: s.IntervalMinutes,
		Proxy:           s.Proxy,
		GitHubTokenSet:  s.GitHubToken != "",
	}
}

func (m *Manager) timestampNow() Timestamp {
	return Timestamp{Time: m.now().UTC().Truncate(time.Second)}
}

// ListMirrors 返回设置视图与全部镜像（含状态副本）
func (m *Manager) ListMirrors() (SettingsView, []MirrorView) {
	var sv SettingsView
	views := []MirrorView{}
	m.store.view(func(d *mirrorData) {
		sv = settingsView(d.Settings)
		for _, mi := range d.Mirrors {
			views = append(views, MirrorView{Mirror: mi.clone(), State: d.stateView(mi.ID)})
		}
	})
	return sv, views
}

// HasMirror 判断镜像是否存在
func (m *Manager) HasMirror(id string) bool {
	found := false
	m.store.view(func(d *mirrorData) { found = d.findMirror(id) >= 0 })
	return found
}

// CreateMirror 新建镜像；enabled 时立即异步同步一次
func (m *Manager) CreateMirror(in MirrorInput) (MirrorView, error) {
	mi, err := normalizeMirrorInput(in, Mirror{Enabled: true, SaveChangelog: true})
	if err != nil {
		return MirrorView{}, err
	}
	var view MirrorView
	err = m.store.update(func(d *mirrorData) error {
		if folderTaken(d.Mirrors, mi.Folder, "") {
			return invalidf("文件夹「%s」已被其它镜像使用", mi.Folder)
		}
		mi.ID = d.newUniqueID()
		mi.CreatedAt = m.timestampNow()
		d.Mirrors = append(d.Mirrors, mi)
		d.State[mi.ID] = newIdleState()
		view = MirrorView{Mirror: mi.clone(), State: newIdleState()}
		return nil
	})
	if err != nil {
		return MirrorView{}, err
	}
	log.Printf("[mirror] created %s (%s) -> %s", mi.Name, mi.Repo, mi.Folder)
	if mi.Enabled {
		m.SyncAsync(mi.ID)
	}
	return view, nil
}

// UpdateMirror 全量更新镜像配置（不移动已有文件）；从禁用变为启用时异步同步
func (m *Manager) UpdateMirror(id string, in MirrorInput) (MirrorView, error) {
	var view MirrorView
	enabledNow := false
	err := m.store.update(func(d *mirrorData) error {
		i := d.findMirror(id)
		if i < 0 {
			return errMirrorNotFound
		}
		old := d.Mirrors[i]
		mi, err := normalizeMirrorInput(in, old)
		if err != nil {
			return err
		}
		if folderTaken(d.Mirrors, mi.Folder, id) {
			return invalidf("文件夹「%s」已被其它镜像使用", mi.Folder)
		}
		mi.ID = old.ID
		mi.CreatedAt = old.CreatedAt
		d.Mirrors[i] = mi
		enabledNow = !old.Enabled && mi.Enabled
		view = MirrorView{Mirror: mi.clone(), State: d.stateView(id)}
		return nil
	})
	if err != nil {
		return MirrorView{}, err
	}
	if enabledNow {
		m.SyncAsync(id)
	}
	return view, nil
}

// DeleteMirror 删除镜像：① 先移除配置与状态（之后的 Sync/Refresh 视为不存在）；② 取消并等待正在进行的同步；
// ③ 直接通过 aria2 RPC 强制移除文件位于 {rootDir}/{folder}/ 下的进行中任务；④ deleteFiles=true 时删除 {rootDir}/{folder}
func (m *Manager) DeleteMirror(id string, deleteFiles bool) error {
	var folderDir string
	err := m.store.update(func(d *mirrorData) error {
		i := d.findMirror(id)
		if i < 0 {
			return errMirrorNotFound
		}
		_, fd, err := m.mirrorPaths(d.Settings.RootDir, d.Mirrors[i].Folder)
		if err == nil {
			folderDir = fd
		} else if deleteFiles {
			return err
		}
		d.Mirrors = append(d.Mirrors[:i], d.Mirrors[i+1:]...)
		delete(d.State, id)
		return nil
	})
	if err != nil {
		return err
	}
	m.cancelSync(id)
	if folderDir == "" {
		return nil
	}
	m.removeAria2Tasks(folderDir)
	if deleteFiles {
		if _, ok := IsSafeSubpath(m.downloadDir, folderDir); !ok {
			return fmt.Errorf("镜像目录不在下载目录内：%s", folderDir)
		}
		if err := os.RemoveAll(folderDir); err != nil {
			return fmt.Errorf("删除镜像文件失败：%v", err)
		}
		log.Printf("[mirror] removed files of mirror %s: %s", id, folderDir)
	}
	return nil
}

// downloadURL 构造 /download/ 直链，每段做 url.PathEscape
func downloadURL(segments ...string) string {
	var esc []string
	for _, s := range segments {
		for _, part := range strings.Split(filepath.ToSlash(s), "/") {
			if part != "" {
				esc = append(esc, url.PathEscape(part))
			}
		}
	}
	return "/download/" + strings.Join(esc, "/")
}

// Versions 列出镜像目录下已有的版本目录及文件（不含 .aria2 控制文件），按修改时间降序
func (m *Manager) Versions(id string) ([]VersionView, error) {
	var folder, root, latestDir string
	found := false
	notDone := map[string]bool{}
	m.store.view(func(d *mirrorData) {
		if i := d.findMirror(id); i >= 0 {
			found = true
			folder = d.Mirrors[i].Folder
			root = d.Settings.RootDir
			if st := d.State[id]; st != nil && st.LatestTag != "" {
				if t, err := sanitizePathName(st.LatestTag); err == nil {
					latestDir = t
					for _, f := range st.Files {
						if f.Status != FileDone {
							notDone[f.Name] = true
						}
					}
				}
			}
		}
	})
	if !found {
		return nil, errMirrorNotFound
	}
	versions := []VersionView{}
	_, folderDir, err := m.mirrorPaths(root, folder)
	if err != nil {
		return versions, nil
	}
	entries, err := os.ReadDir(folderDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return versions, nil
		}
		return nil, fmt.Errorf("读取镜像目录失败：%v", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		v := VersionView{
			Tag:     e.Name(),
			ModTime: Timestamp{Time: info.ModTime()},
			Latest:  latestDir != "" && e.Name() == latestDir,
			Files:   []VersionFile{},
		}
		files, err := os.ReadDir(filepath.Join(folderDir, e.Name()))
		if err != nil {
			continue
		}
		names := make(map[string]bool, len(files))
		for _, f := range files {
			names[f.Name()] = true
		}
		for _, f := range files {
			if !f.Type().IsRegular() || strings.HasSuffix(f.Name(), aria2ControlFileSuffix) {
				continue
			}
			fi, err := f.Info()
			if err != nil {
				continue
			}
			v.Files = append(v.Files, VersionFile{
				Name:    f.Name(),
				Size:    fi.Size(),
				URL:     downloadURL(root, folder, e.Name(), f.Name()),
				Partial: names[f.Name()+aria2ControlFileSuffix] || (v.Latest && notDone[f.Name()]),
			})
		}
		versions = append(versions, v)
	}
	sort.SliceStable(versions, func(i, j int) bool {
		if !versions[i].ModTime.Equal(versions[j].ModTime.Time) {
			return versions[i].ModTime.After(versions[j].ModTime.Time)
		}
		return versions[i].Tag > versions[j].Tag
	})
	return versions, nil
}

// Settings 返回设置视图
func (m *Manager) Settings() SettingsView {
	var sv SettingsView
	m.store.view(func(d *mirrorData) { sv = settingsView(d.Settings) })
	return sv
}

// UpdateSettings 校验并保存设置
func (m *Manager) UpdateSettings(in SettingsInput) (SettingsView, error) {
	var sv SettingsView
	err := m.store.update(func(d *mirrorData) error {
		s := d.Settings
		if in.RootDir != nil {
			r, err := normalizeRootDir(*in.RootDir)
			if err != nil {
				return err
			}
			s.RootDir = r
		}
		if in.IntervalMinutes != nil {
			n := int(*in.IntervalMinutes)
			if err := validateInterval(n); err != nil {
				return err
			}
			s.IntervalMinutes = n
		}
		if in.Proxy != nil {
			p, err := normalizeProxy(*in.Proxy)
			if err != nil {
				return err
			}
			s.Proxy = p
		}
		if in.GitHubToken != nil {
			t := strings.TrimSpace(*in.GitHubToken)
			if hasControlChar(t) || strings.ContainsAny(t, " \t") {
				return invalidf("GitHub Token 包含非法字符")
			}
			s.GitHubToken = t
		}
		d.Settings = s
		sv = settingsView(s)
		return nil
	})
	return sv, err
}

// ---- HTTP 层 ----

type mirrorAPI struct {
	m *Manager
}

// RegisterMirrorAPI 在 mux 上注册 /api/ 下的镜像管理接口（Go 1.22 方法路由）
func RegisterMirrorAPI(mux *http.ServeMux, m *Manager) {
	a := &mirrorAPI{m: m}
	mux.HandleFunc("GET /api/mirrors", a.list)
	mux.HandleFunc("POST /api/mirrors", a.create)
	mux.HandleFunc("PUT /api/mirrors/{id}", a.update)
	mux.HandleFunc("DELETE /api/mirrors/{id}", a.remove)
	mux.HandleFunc("POST /api/mirrors/{id}/sync", a.syncOne)
	mux.HandleFunc("POST /api/mirrors/sync", a.syncAll)
	mux.HandleFunc("GET /api/mirrors/{id}/versions", a.versions)
	mux.HandleFunc("GET /api/mirror-settings", a.getSettings)
	mux.HandleFunc("PUT /api/mirror-settings", a.putSettings)
	mux.HandleFunc("POST /api/mirrors/import", a.importYAML)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeJSONError(w, http.StatusNotFound, "接口不存在")
	})
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	b, err := json.Marshal(v)
	if err != nil {
		code = http.StatusInternalServerError
		b = []byte(`{"error":"内部错误"}`)
	}
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(code)
	w.Write(b)
	w.Write([]byte("\n"))
}

func writeJSONError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func writeAPIError(w http.ResponseWriter, err error) {
	var ve *validationError
	switch {
	case errors.As(err, &ve):
		writeJSONError(w, http.StatusBadRequest, ve.Error())
	case errors.Is(err, errMirrorNotFound):
		writeJSONError(w, http.StatusNotFound, errMirrorNotFound.Error())
	default:
		log.Printf("[mirror] api error: %v", err)
		writeJSONError(w, http.StatusInternalServerError, err.Error())
	}
}

func isJSONContentType(ct string) bool {
	if ct == "" {
		return false
	}
	mt, _, err := mime.ParseMediaType(ct)
	return err == nil && strings.EqualFold(mt, "application/json")
}

// decodeJSONBody 校验 Content-Type（CSRF 防护）并以 1 MiB 上限解析请求体；出错时已写出响应并返回 false
func decodeJSONBody(w http.ResponseWriter, r *http.Request, v interface{}, allowEmpty bool) bool {
	if !isJSONContentType(r.Header.Get("Content-Type")) {
		writeJSONError(w, http.StatusUnsupportedMediaType, "Content-Type 必须为 application/json")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAPIBodyBytes)
	err := json.NewDecoder(r.Body).Decode(v)
	if err == nil {
		return true
	}
	var mbe *http.MaxBytesError
	var ve *validationError
	switch {
	case errors.Is(err, io.EOF) && allowEmpty:
		return true
	case errors.As(err, &mbe):
		writeJSONError(w, http.StatusRequestEntityTooLarge, "请求体超过 1 MiB 上限")
	case errors.As(err, &ve):
		writeJSONError(w, http.StatusBadRequest, ve.Error())
	case errors.Is(err, io.EOF):
		writeJSONError(w, http.StatusBadRequest, "请求体不能为空")
	default:
		writeJSONError(w, http.StatusBadRequest, "请求体不是有效的 JSON："+err.Error())
	}
	return false
}

func (a *mirrorAPI) list(w http.ResponseWriter, r *http.Request) {
	// 刷新下载进度为尽力而为，失败不影响返回
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	_ = a.m.RefreshProgress(ctx)
	cancel()
	sv, views := a.m.ListMirrors()
	writeJSON(w, http.StatusOK, mirrorListResponse{Settings: sv, DownloadDir: a.m.downloadDir, Mirrors: views})
}

func (a *mirrorAPI) create(w http.ResponseWriter, r *http.Request) {
	var in MirrorInput
	if !decodeJSONBody(w, r, &in, false) {
		return
	}
	view, err := a.m.CreateMirror(in)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, view)
}

func (a *mirrorAPI) update(w http.ResponseWriter, r *http.Request) {
	var in MirrorInput
	if !decodeJSONBody(w, r, &in, false) {
		return
	}
	view, err := a.m.UpdateMirror(r.PathValue("id"), in)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (a *mirrorAPI) remove(w http.ResponseWriter, r *http.Request) {
	deleteFiles := false
	switch strings.ToLower(r.URL.Query().Get("deleteFiles")) {
	case "1", "true", "yes", "on":
		deleteFiles = true
	}
	if err := a.m.DeleteMirror(r.PathValue("id"), deleteFiles); err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *mirrorAPI) syncOne(w http.ResponseWriter, r *http.Request) {
	var body json.RawMessage
	if !decodeJSONBody(w, r, &body, true) {
		return
	}
	id := r.PathValue("id")
	if !a.m.HasMirror(id) {
		writeAPIError(w, errMirrorNotFound)
		return
	}
	a.m.SyncAsync(id)
	writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
}

func (a *mirrorAPI) syncAll(w http.ResponseWriter, r *http.Request) {
	var body json.RawMessage
	if !decodeJSONBody(w, r, &body, true) {
		return
	}
	a.m.SyncAllAsync()
	writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
}

func (a *mirrorAPI) versions(w http.ResponseWriter, r *http.Request) {
	versions, err := a.m.Versions(r.PathValue("id"))
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string][]VersionView{"versions": versions})
}

func (a *mirrorAPI) getSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.m.Settings())
}

func (a *mirrorAPI) putSettings(w http.ResponseWriter, r *http.Request) {
	var in SettingsInput
	if !decodeJSONBody(w, r, &in, false) {
		return
	}
	sv, err := a.m.UpdateSettings(in)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sv)
}

func (a *mirrorAPI) importYAML(w http.ResponseWriter, r *http.Request) {
	var in struct {
		YAML string `json:"yaml"`
	}
	if !decodeJSONBody(w, r, &in, false) {
		return
	}
	res, err := a.m.ImportYAML(in.YAML)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
