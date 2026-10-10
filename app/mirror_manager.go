package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const defaultGitHubAPI = "https://api.github.com"

var (
	errMirrorNotFound = errors.New("镜像不存在")
	errSyncInProgress = errors.New("该镜像正在同步中")
)

// ManagerConfig 镜像管理器配置（测试时可注入 mock 地址、时钟与调度间隔）
type ManagerConfig struct {
	DownloadDir   string
	ConfigDir     string
	GitHubAPI     string
	Aria2URL      string
	Aria2Secret   string
	EnvToken      string // GITHUB_TOKEN 环境变量，设置中未填写 Token 时使用
	GitHubTimeout time.Duration
	Location      *time.Location // 限流重置时间的显示时区，默认 time.Local
	Now           func() time.Time
	InitialDelay  time.Duration // 调度器启动延迟，默认 20s
	TickInterval  time.Duration // 调度器 tick 间隔，默认 30s
}

// Manager 负责 GitHub Release 镜像的同步、进度刷新、保留策略与定时调度
type Manager struct {
	cfg         ManagerConfig
	downloadDir string
	store       *MirrorStore
	aria2       *Aria2Client
	now         func() time.Time

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	syncMu         sync.Mutex
	syncing        map[string]*syncHandle
	syncAllRunning bool
	closed         bool

	// githubHostOnly 使用默认 GitHub API 时，资产下载地址的主机必须是 github.com
	githubHostOnly bool

	// refreshHook 测试钩子：RefreshProgress 查询 aria2 之后、写回状态之前调用
	refreshHook func()
}

// syncHandle 正在运行的同步：cancel 取消其 context，done 在同步结束时关闭
type syncHandle struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// NewManager 创建管理器；配置文件损坏/不可读时返回错误，但返回的管理器仍可用（使用默认配置）
func NewManager(cfg ManagerConfig) (*Manager, error) {
	if cfg.DownloadDir == "" {
		cfg.DownloadDir = "/downloads"
	}
	if cfg.ConfigDir == "" {
		cfg.ConfigDir = "/config"
	}
	if cfg.GitHubAPI == "" {
		cfg.GitHubAPI = defaultGitHubAPI
	}
	if cfg.Aria2URL == "" {
		cfg.Aria2URL = "http://127.0.0.1:6800/jsonrpc"
	}
	if cfg.GitHubTimeout <= 0 {
		cfg.GitHubTimeout = 30 * time.Second
	}
	if cfg.Location == nil {
		cfg.Location = time.Local
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.InitialDelay <= 0 {
		cfg.InitialDelay = 20 * time.Second
	}
	if cfg.TickInterval <= 0 {
		cfg.TickInterval = 30 * time.Second
	}
	store, err := OpenMirrorStore(cfg.ConfigDir)
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{
		cfg:            cfg,
		downloadDir:    filepath.Clean(cfg.DownloadDir),
		store:          store,
		aria2:          NewAria2Client(cfg.Aria2URL, cfg.Aria2Secret),
		now:            cfg.Now,
		ctx:            ctx,
		cancel:         cancel,
		syncing:        map[string]*syncHandle{},
		githubHostOnly: strings.EqualFold(strings.TrimRight(cfg.GitHubAPI, "/"), defaultGitHubAPI),
	}
	return m, err
}

// Close 停止异步同步并等待其退出（测试用）
func (m *Manager) Close() {
	m.syncMu.Lock()
	m.closed = true
	m.syncMu.Unlock()
	m.cancel()
	m.wg.Wait()
}

// Wait 等待当前所有异步同步结束（测试用）
func (m *Manager) Wait() {
	m.wg.Wait()
}

func (m *Manager) githubClient(settings MirrorSettings) *GitHubClient {
	token := settings.GitHubToken
	if token == "" {
		token = m.cfg.EnvToken
	}
	return &GitHubClient{
		BaseURL:  m.cfg.GitHubAPI,
		Token:    token,
		Proxy:    settings.Proxy,
		Timeout:  m.cfg.GitHubTimeout,
		Location: m.cfg.Location,
	}
}

// mirrorPaths 计算 {downloadDir}/{rootDir} 与 {downloadDir}/{rootDir}/{folder}，并做路径安全校验
func (m *Manager) mirrorPaths(rootDir, folder string) (rootBase, folderDir string, err error) {
	root, err := normalizeRootDir(rootDir)
	if err != nil {
		return "", "", err
	}
	if f, ferr := normalizeFolder(folder, "_/_"); ferr != nil || f != folder {
		return "", "", fmt.Errorf("镜像文件夹名称无效：%q", folder)
	}
	rootBase = filepath.Join(m.downloadDir, filepath.FromSlash(root))
	if _, ok := IsSafeSubpath(m.downloadDir, rootBase); !ok {
		return "", "", fmt.Errorf("根目录不在下载目录内：%s", root)
	}
	folderDir = filepath.Join(rootBase, folder)
	if _, ok := IsSafeSubpath(rootBase, folderDir); !ok || filepath.Dir(folderDir) != rootBase {
		return "", "", fmt.Errorf("镜像文件夹路径无效：%q", folder)
	}
	return rootBase, folderDir, nil
}

// versionPath 计算 {folderDir}/{safeTag}，tag 中的 / \ 替换为 _
func versionPath(folderDir, tag string) (safeTag, dir string, err error) {
	safeTag, err = sanitizePathName(tag)
	if err != nil {
		return "", "", fmt.Errorf("Release tag 无法作为目录名：%q", tag)
	}
	dir = filepath.Join(folderDir, safeTag)
	if filepath.Dir(dir) != filepath.Clean(folderDir) {
		return "", "", fmt.Errorf("Release tag 无法作为目录名：%q", tag)
	}
	return safeTag, dir, nil
}

// IsManagedPath 判断路径是否位于 {downloadDir}/{当前 rootDir}/ 之下（镜像文件不随 aria2 任务删除）
func (m *Manager) IsManagedPath(p string) bool {
	if p == "" {
		return false
	}
	var root string
	m.store.view(func(d *mirrorData) { root = d.Settings.RootDir })
	base := filepath.Join(m.downloadDir, filepath.FromSlash(root))
	if _, ok := IsSafeSubpath(m.downloadDir, base); !ok {
		return false
	}
	_, ok := IsSafeSubpath(base, p)
	return ok
}

// fileComplete 文件存在、无 .aria2 控制文件、大小与资产一致
func fileComplete(p string, size int64) bool {
	fi, err := os.Stat(p)
	if err != nil || !fi.Mode().IsRegular() {
		return false
	}
	if _, err := os.Lstat(p + aria2ControlFileSuffix); !errors.Is(err, os.ErrNotExist) {
		return false
	}
	return fi.Size() == size
}

// checkAssetURL 资产下载地址必须为 http/https；使用默认 GitHub API 时主机还必须是 github.com
// （防止被篡改的元数据把 aria2 引向内网）
func (m *Manager) checkAssetURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("下载地址不是 http/https 地址，已跳过")
	}
	if m.githubHostOnly && !strings.EqualFold(u.Host, "github.com") {
		return fmt.Errorf("下载地址的主机 %s 不是 github.com，已跳过", u.Host)
	}
	return nil
}

// stoppedErrorLookup 为 pending 文件补全 aria2 失败原因；最多调用一次 aria2.tellStopped
type stoppedErrorLookup struct {
	aria2  *Aria2Client
	loaded bool
	errs   map[string]string
}

// fill 只填写尚无 error 的 pending 文件；查询失败时保持原样
func (l *stoppedErrorLookup) fill(ctx context.Context, versionDir string, files []MirrorFile) {
	if versionDir == "" {
		return
	}
	for i := range files {
		f := &files[i]
		if f.Status != FilePending || f.Error != "" {
			continue
		}
		if !l.loaded {
			l.loaded = true
			errs, err := l.aria2.StoppedErrors(ctx)
			if err != nil {
				log.Printf("[mirror] query aria2 stopped tasks failed: %v", err)
			}
			l.errs = errs
		}
		if msg, ok := l.errs[filepath.Join(versionDir, f.Name)]; ok {
			f.Error = msg
		}
	}
}

// incompleteError 汇总带 error 的 pending 文件作为 incomplete 镜像的 lastError
func incompleteError(files []MirrorFile) string {
	var parts []string
	for _, f := range files {
		if f.Status == FilePending && f.Error != "" {
			parts = append(parts, f.Name+"："+f.Error)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "部分文件下载失败：" + strings.Join(parts, "；")
}

// summarizeFiles 全部 done → synced；有 downloading → downloading；否则 incomplete
func summarizeFiles(files []MirrorFile) string {
	downloading := false
	allDone := true
	for _, f := range files {
		switch f.Status {
		case FileDone:
		case FileDownloading:
			downloading = true
			allDone = false
		default:
			allDone = false
		}
	}
	switch {
	case allDone:
		return StatusSynced
	case downloading:
		return StatusDownloading
	default:
		return StatusIncomplete
	}
}

// writeChangelog 仅在文件不存在时写出
func writeChangelog(p, body string) error {
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil
		}
		return fmt.Errorf("写入 %s 失败：%v", changelogFileName, err)
	}
	if _, err := f.WriteString(body); err != nil {
		f.Close()
		os.Remove(p)
		return fmt.Errorf("写入 %s 失败：%v", changelogFileName, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("写入 %s 失败：%v", changelogFileName, err)
	}
	return nil
}

// tryBeginSync 登记同步并返回可被 DeleteMirror 取消的 context；已在同步时返回 false
func (m *Manager) tryBeginSync(ctx context.Context, id string) (context.Context, bool) {
	m.syncMu.Lock()
	defer m.syncMu.Unlock()
	if m.syncing[id] != nil {
		return nil, false
	}
	sctx, cancel := context.WithCancel(ctx)
	m.syncing[id] = &syncHandle{cancel: cancel, done: make(chan struct{})}
	return sctx, true
}

func (m *Manager) endSync(id string) {
	m.syncMu.Lock()
	h := m.syncing[id]
	delete(m.syncing, id)
	m.syncMu.Unlock()
	if h != nil {
		h.cancel()
		close(h.done)
	}
}

// cancelSync 取消指定镜像正在进行的同步并等待其结束
func (m *Manager) cancelSync(id string) {
	m.syncMu.Lock()
	h := m.syncing[id]
	m.syncMu.Unlock()
	if h != nil {
		h.cancel()
		<-h.done
	}
}

func (m *Manager) syncingSnapshot() map[string]bool {
	m.syncMu.Lock()
	defer m.syncMu.Unlock()
	s := make(map[string]bool, len(m.syncing))
	for id := range m.syncing {
		s[id] = true
	}
	return s
}

// Sync 检查并镜像指定镜像的最新 Release；同一镜像正在同步时返回 errSyncInProgress
func (m *Manager) Sync(ctx context.Context, id string) error {
	sctx, ok := m.tryBeginSync(ctx, id)
	if !ok {
		return errSyncInProgress
	}
	defer m.endSync(id)
	return m.runSync(sctx, id)
}

type syncResult struct {
	release    *GitHubRelease
	files      []MirrorFile
	addErrors  []string
	folderDir  string
	safeTag    string
	versionDir string
	inProgress map[string]int64
}

func (m *Manager) runSync(ctx context.Context, id string) error {
	var mirror Mirror
	var settings MirrorSettings
	if err := m.store.updateState(func(d *mirrorData) (bool, error) {
		i := d.findMirror(id)
		if i < 0 {
			return false, errMirrorNotFound
		}
		mirror = d.Mirrors[i].clone()
		settings = d.Settings
		st := d.stateFor(id)
		st.Status = StatusChecking
		st.LastCheck = Timestamp{Time: m.now()}
		st.rev++
		return true, nil
	}); err != nil {
		return err
	}

	res, err := m.fetchAndQueue(ctx, id, mirror, settings)
	if err != nil {
		log.Printf("[mirror] %s (%s) sync failed: %v", mirror.Name, mirror.Repo, err)
		m.store.updateState(func(d *mirrorData) (bool, error) {
			if d.findMirror(id) < 0 {
				return false, nil
			}
			st := d.stateFor(id)
			st.Status = StatusError
			st.LastError = err.Error()
			st.rev++
			return true, nil
		})
		return err
	}

	now := m.now()
	status := summarizeFiles(res.files)
	lastErr := ""
	if len(res.addErrors) > 0 {
		status = StatusError
		lastErr = "部分文件投递到 aria2 失败：" + strings.Join(res.addErrors, "；")
	} else if status == StatusIncomplete {
		(&stoppedErrorLookup{aria2: m.aria2}).fill(ctx, res.versionDir, res.files)
		lastErr = incompleteError(res.files)
	}
	rel := res.release
	m.store.updateState(func(d *mirrorData) (bool, error) {
		if d.findMirror(id) < 0 {
			// 同步期间镜像已被删除
			return false, nil
		}
		st := d.stateFor(id)
		st.Status = status
		st.LatestTag = rel.TagName
		st.ReleaseName = rel.Name
		st.ReleaseURL = rel.HTMLURL
		st.PublishedAt = rel.PublishedTime()
		st.Files = res.files
		st.LastError = lastErr
		if status == StatusSynced {
			st.LastSuccess = Timestamp{Time: now}
		}
		st.rev++
		return true, nil
	})
	log.Printf("[mirror] %s (%s) %s: %d file(s), status %s", mirror.Name, mirror.Repo, rel.TagName, len(res.files), status)

	// 版本保留只在当前版本已完整同步后执行；仍在下载时由 RefreshProgress 在变为 synced 时执行
	if status == StatusSynced {
		m.retainVersions(id, res.folderDir, res.safeTag, res.inProgress)
	}
	if len(res.addErrors) > 0 {
		return errors.New(lastErr)
	}
	return nil
}

type queuedAsset struct {
	asset  GitHubAsset
	name   string
	target string
}

// fetchAndQueue 拉取 Release、写 CHANGELOG、把缺失的资产投递给 aria2（网络请求不持有 store 锁）
func (m *Manager) fetchAndQueue(ctx context.Context, id string, mirror Mirror, settings MirrorSettings) (*syncResult, error) {
	re, err := compileMirrorRegex(mirror.Regex)
	if err != nil {
		return nil, err
	}
	_, folderDir, err := m.mirrorPaths(settings.RootDir, mirror.Folder)
	if err != nil {
		return nil, err
	}

	rel, err := m.githubClient(settings).LatestRelease(ctx, mirror.Repo, mirror.IncludePrerelease)
	if err != nil {
		return nil, err
	}

	safeTag, versionDir, err := versionPath(folderDir, rel.TagName)
	if err != nil {
		return nil, err
	}
	if _, ok := IsSafeSubpath(m.downloadDir, versionDir); !ok {
		return nil, fmt.Errorf("版本目录不在下载目录内：%s", versionDir)
	}

	var assets []queuedAsset
	seen := map[string]bool{}
	hasChangelogAsset := false
	for _, a := range rel.Assets {
		if !matchAsset(a.Name, mirror.FileTypes, re) {
			continue
		}
		name, err := sanitizePathName(a.Name)
		if err != nil {
			log.Printf("[mirror] %s: skip asset with invalid name %q", mirror.Repo, a.Name)
			continue
		}
		target := filepath.Join(versionDir, name)
		if seen[name] || filepath.Dir(target) != versionDir {
			continue
		}
		if _, ok := IsSafeSubpath(m.downloadDir, target); !ok {
			continue
		}
		seen[name] = true
		if strings.EqualFold(name, changelogFileName) {
			hasChangelogAsset = true
		}
		assets = append(assets, queuedAsset{asset: a, name: name, target: target})
	}

	// 在 store 锁内再次确认镜像仍存在（同步期间可能已被删除），存在才创建目录、写 CHANGELOG
	var prepErr error
	m.store.view(func(d *mirrorData) {
		if d.findMirror(id) < 0 {
			prepErr = errMirrorNotFound
			return
		}
		if err := os.MkdirAll(versionDir, 0755); err != nil {
			prepErr = fmt.Errorf("创建目录失败：%v", err)
			return
		}
		// 资产中已有同名 CHANGELOG.md 时以资产为准，不写出 Release 说明
		if mirror.SaveChangelog && rel.Body != "" && !hasChangelogAsset {
			prepErr = writeChangelog(filepath.Join(versionDir, changelogFileName), rel.Body)
		}
	})
	if prepErr != nil {
		return nil, prepErr
	}

	inProgress, err := m.aria2.InProgress(ctx)
	if err != nil {
		return nil, fmt.Errorf("查询 aria2 下载任务失败：%v", err)
	}

	files := []MirrorFile{}
	var addErrors []string
	for _, q := range assets {
		a, name, target := q.asset, q.name, q.target
		f := MirrorFile{Name: name, Size: a.Size, Status: FilePending}
		if fileComplete(target, a.Size) {
			f.Status = FileDone
			f.CompletedLength = a.Size
		} else if done, ok := inProgress[target]; ok {
			f.Status = FileDownloading
			f.CompletedLength = done
		} else if a.BrowserDownloadURL == "" {
			f.Error = "缺少下载地址"
			addErrors = append(addErrors, name+"：缺少下载地址")
		} else if err := m.checkAssetURL(a.BrowserDownloadURL); err != nil {
			log.Printf("[mirror] %s: skip asset %q (%s): %v", mirror.Repo, name, a.BrowserDownloadURL, err)
			f.Error = err.Error()
		} else {
			opts := map[string]string{
				"dir":                versionDir,
				"out":                name,
				"allow-overwrite":    "true",
				"auto-file-renaming": "false",
				// 全局 aria2.conf 为 continue=true：无控制文件的旧文件会被当作已下载前缀续传，必须关闭
				"continue": "false",
				// 资产只作为普通文件保存，不解析 .meta4/.metalink/.torrent 内容去下载其它地址（SSRF）
				"follow-metalink": "false",
				"follow-torrent":  "false",
			}
			if settings.Proxy != "" {
				opts["all-proxy"] = settings.Proxy
			}
			if _, err := m.aria2.AddURI(ctx, []string{a.BrowserDownloadURL}, opts); err != nil {
				f.Error = err.Error()
				addErrors = append(addErrors, fmt.Sprintf("%s：%v", name, err))
			} else {
				f.Status = FileDownloading
				inProgress[target] = 0
			}
		}
		files = append(files, f)
	}

	return &syncResult{
		release:    rel,
		files:      files,
		addErrors:  addErrors,
		folderDir:  folderDir,
		safeTag:    safeTag,
		versionDir: versionDir,
		inProgress: inProgress,
	}, nil
}

// retainVersions 在 store 锁内确认镜像仍存在、文件夹未变后执行版本保留（镜像已删除时不执行）
func (m *Manager) retainVersions(id, folderDir, safeTag string, inProgress map[string]int64) {
	m.store.view(func(d *mirrorData) {
		i := d.findMirror(id)
		if i < 0 || d.Mirrors[i].KeepVersions <= 0 {
			return
		}
		_, fd, err := m.mirrorPaths(d.Settings.RootDir, d.Mirrors[i].Folder)
		if err != nil || fd != filepath.Clean(folderDir) {
			return
		}
		m.applyRetention(fd, safeTag, d.Mirrors[i].KeepVersions, inProgress)
	})
}

// removeAria2Tasks 直接通过 aria2 RPC 强制移除文件位于 dir 下的 active/waiting/paused 任务（尽力而为）
func (m *Manager) removeAria2Tasks(dir string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	tasks, err := m.aria2.ActiveTasks(ctx)
	if err != nil {
		log.Printf("[mirror] query aria2 tasks under %s failed: %v", dir, err)
		return
	}
	for _, t := range tasks {
		under := false
		for _, f := range t.Files {
			if _, ok := IsSafeSubpath(dir, f.Path); ok && f.Path != "" {
				under = true
				break
			}
		}
		if !under || t.GID == "" {
			continue
		}
		if err := m.aria2.ForceRemove(ctx, t.GID); err != nil {
			log.Printf("[mirror] remove aria2 task %s failed: %v", t.GID, err)
		} else {
			log.Printf("[mirror] removed aria2 task %s under %s", t.GID, dir)
		}
	}
}

// applyRetention 保留当前版本 + 最新的 keep-1 个旧版本目录（按 mtime），跳过仍有 aria2 任务的目录
func (m *Manager) applyRetention(folderDir, currentTag string, keep int, inProgress map[string]int64) {
	folderDir = filepath.Clean(folderDir)
	if _, ok := IsSafeSubpath(m.downloadDir, folderDir); !ok {
		return
	}
	entries, err := os.ReadDir(folderDir)
	if err != nil {
		log.Printf("[mirror] retention: read %s failed: %v", folderDir, err)
		return
	}
	type version struct {
		name string
		mod  time.Time
	}
	var others []version
	for _, e := range entries {
		// 符号链接的 IsDir() 为 false，不会被处理
		if !e.IsDir() || e.Name() == currentTag {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		others = append(others, version{name: e.Name(), mod: info.ModTime()})
	}
	sort.Slice(others, func(i, j int) bool {
		if !others[i].mod.Equal(others[j].mod) {
			return others[i].mod.After(others[j].mod)
		}
		return others[i].name > others[j].name
	})
	if keep-1 >= len(others) {
		return
	}
	for _, v := range others[keep-1:] {
		p := filepath.Join(folderDir, v.name)
		if filepath.Dir(p) != folderDir {
			continue
		}
		if _, ok := IsSafeSubpath(folderDir, p); !ok {
			continue
		}
		if _, ok := IsSafeSubpath(m.downloadDir, p); !ok {
			continue
		}
		if dirHasInProgress(p, inProgress) {
			log.Printf("[mirror] retention: keep %s (download in progress)", p)
			continue
		}
		if err := os.RemoveAll(p); err != nil {
			log.Printf("[mirror] retention: remove %s failed: %v", p, err)
		} else {
			log.Printf("[mirror] retention: removed old version %s", p)
		}
	}
}

func dirHasInProgress(dir string, inProgress map[string]int64) bool {
	for p := range inProgress {
		if _, ok := IsSafeSubpath(dir, p); ok {
			return true
		}
	}
	return false
}

// RefreshProgress 不访问 GitHub，仅依据磁盘与 aria2 任务列表刷新 downloading 镜像的文件状态
func (m *Manager) RefreshProgress(ctx context.Context) error {
	type candidate struct {
		id         string
		rev        uint64
		folderDir  string
		safeTag    string
		versionDir string
		files      []MirrorFile
	}
	syncing := m.syncingSnapshot()
	var cands []candidate
	m.store.view(func(d *mirrorData) {
		for _, mi := range d.Mirrors {
			st := d.State[mi.ID]
			if st == nil || st.Status != StatusDownloading || syncing[mi.ID] {
				continue
			}
			c := candidate{id: mi.ID, rev: st.rev, files: append([]MirrorFile{}, st.Files...)}
			if _, folderDir, err := m.mirrorPaths(d.Settings.RootDir, mi.Folder); err == nil {
				if safeTag, vdir, err := versionPath(folderDir, st.LatestTag); err == nil {
					c.folderDir, c.safeTag, c.versionDir = folderDir, safeTag, vdir
				}
			}
			cands = append(cands, c)
		}
	})
	if len(cands) == 0 {
		return nil
	}

	inProgress, err := m.aria2.InProgress(ctx)
	if err != nil {
		return fmt.Errorf("查询 aria2 下载任务失败：%v", err)
	}
	if m.refreshHook != nil {
		m.refreshHook()
	}
	now := m.now()
	stopped := &stoppedErrorLookup{aria2: m.aria2}
	for _, c := range cands {
		files := make([]MirrorFile, len(c.files))
		for i, f := range c.files {
			// pending 文件保留已有的 error（如下载地址被拒绝）
			nf := f
			nf.Status = FilePending
			nf.CompletedLength = 0
			if c.versionDir != "" {
				p := filepath.Join(c.versionDir, f.Name)
				if filepath.Dir(p) == c.versionDir {
					if fileComplete(p, f.Size) {
						nf.Status = FileDone
						nf.CompletedLength = f.Size
						nf.Error = ""
					} else if done, ok := inProgress[p]; ok {
						nf.Status = FileDownloading
						nf.CompletedLength = done
						nf.Error = ""
					}
				}
			}
			files[i] = nf
		}
		status := summarizeFiles(files)
		lastErr := ""
		if status == StatusIncomplete {
			stopped.fill(ctx, c.versionDir, files)
			lastErr = incompleteError(files)
		}
		applied := false
		m.store.updateState(func(d *mirrorData) (bool, error) {
			st := d.State[c.id]
			if st == nil || st.rev != c.rev || st.Status != StatusDownloading || d.findMirror(c.id) < 0 {
				// 期间已有新的同步写入或镜像已删除，丢弃本次结果
				return false, nil
			}
			changed := status != st.Status || fileStatusChanged(st.Files, files)
			st.Status = status
			st.Files = files
			st.LastError = lastErr
			if status == StatusSynced {
				st.LastSuccess = Timestamp{Time: now}
			}
			st.rev++
			applied = true
			// 仅进度字节变化时只更新内存，避免轮询时频繁写盘
			return changed, nil
		})
		// 刚变为 synced：执行版本保留
		if applied && status == StatusSynced {
			m.retainVersions(c.id, c.folderDir, c.safeTag, inProgress)
		}
	}
	return nil
}

func fileStatusChanged(a, b []MirrorFile) bool {
	if len(a) != len(b) {
		return true
	}
	for i := range a {
		if a[i].Name != b[i].Name || a[i].Status != b[i].Status || a[i].Error != b[i].Error {
			return true
		}
	}
	return false
}

// dueMirrors 返回到期需要检查的已启用镜像
func (m *Manager) dueMirrors(now time.Time) []string {
	var ids []string
	m.store.view(func(d *mirrorData) {
		interval := time.Duration(d.Settings.IntervalMinutes) * time.Minute
		if interval <= 0 {
			return
		}
		for _, mi := range d.Mirrors {
			if !mi.Enabled {
				continue
			}
			st := d.State[mi.ID]
			if st == nil || st.LastCheck.IsZero() {
				ids = append(ids, mi.ID)
				continue
			}
			elapsed := now.Sub(st.LastCheck.Time)
			// 时钟回拨（lastCheck 在未来）时也视为到期
			if elapsed >= interval || elapsed < -time.Minute {
				ids = append(ids, mi.ID)
			}
		}
	})
	return ids
}

func (m *Manager) enabledMirrorIDs() []string {
	var ids []string
	m.store.view(func(d *mirrorData) {
		for _, mi := range d.Mirrors {
			if mi.Enabled {
				ids = append(ids, mi.ID)
			}
		}
	})
	return ids
}

// Run 调度器：延迟 InitialDelay 后开始，每 TickInterval 刷新进度并同步到期镜像；ctx 取消时退出
func (m *Manager) Run(ctx context.Context) {
	timer := time.NewTimer(m.cfg.InitialDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
	}
	ticker := time.NewTicker(m.cfg.TickInterval)
	defer ticker.Stop()
	for {
		m.tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (m *Manager) tick(ctx context.Context) {
	if err := m.RefreshProgress(ctx); err != nil {
		log.Printf("[mirror] refresh progress: %v", err)
	}
	for _, id := range m.dueMirrors(m.now()) {
		if ctx.Err() != nil {
			return
		}
		// 失败已在 runSync 中记录日志并写入状态
		_ = m.Sync(ctx, id)
	}
}

// goAsync 在管理器生命周期内启动后台任务；Close 之后不再启动
func (m *Manager) goAsync(fn func(ctx context.Context)) bool {
	m.syncMu.Lock()
	defer m.syncMu.Unlock()
	if m.closed {
		return false
	}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		fn(m.ctx)
	}()
	return true
}

// SyncAsync 异步同步单个镜像（正在同步时忽略）
func (m *Manager) SyncAsync(id string) {
	m.goAsync(func(ctx context.Context) {
		_ = m.Sync(ctx, id)
	})
}

// SyncAllAsync 异步顺序同步所有已启用镜像；已有全部检查在运行时忽略
func (m *Manager) SyncAllAsync() {
	m.syncMu.Lock()
	if m.syncAllRunning {
		m.syncMu.Unlock()
		return
	}
	m.syncAllRunning = true
	m.syncMu.Unlock()
	started := m.goAsync(func(ctx context.Context) {
		defer func() {
			m.syncMu.Lock()
			m.syncAllRunning = false
			m.syncMu.Unlock()
		}()
		for _, id := range m.enabledMirrorIDs() {
			if ctx.Err() != nil {
				return
			}
			_ = m.Sync(ctx, id)
		}
	})
	if !started {
		m.syncMu.Lock()
		m.syncAllRunning = false
		m.syncMu.Unlock()
	}
}
