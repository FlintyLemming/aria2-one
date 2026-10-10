package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	mirrorStoreFile        = "github-mirror.json"
	defaultMirrorRootDir   = "github-mirror"
	defaultMirrorInterval  = 360
	minMirrorInterval      = 10
	maxMirrorInterval      = 43200
	maxMirrorFolderLength  = 128
	changelogFileName      = "CHANGELOG.md"
	aria2ControlFileSuffix = ".aria2"
)

// 镜像状态取值
const (
	StatusIdle        = "idle"
	StatusChecking    = "checking"
	StatusDownloading = "downloading"
	StatusSynced      = "synced"
	StatusIncomplete  = "incomplete"
	StatusError       = "error"
)

// 文件状态取值
const (
	FileDone        = "done"
	FileDownloading = "downloading"
	FilePending     = "pending"
)

// Timestamp 零值序列化为 ""，便于前端判断"从未发生"
type Timestamp struct {
	time.Time
}

func (t Timestamp) MarshalJSON() ([]byte, error) {
	if t.IsZero() {
		return []byte(`""`), nil
	}
	return json.Marshal(t.UTC().Format(time.RFC3339))
}

func (t *Timestamp) UnmarshalJSON(b []byte) error {
	var s *string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	if s == nil || *s == "" {
		t.Time = time.Time{}
		return nil
	}
	parsed, err := time.Parse(time.RFC3339, *s)
	if err != nil {
		return err
	}
	t.Time = parsed
	return nil
}

type MirrorSettings struct {
	RootDir         string `json:"rootDir"`
	IntervalMinutes int    `json:"intervalMinutes"`
	Proxy           string `json:"proxy"`
	GitHubToken     string `json:"githubToken"`
}

type Mirror struct {
	ID                string    `json:"id"`
	Name              string    `json:"name"`
	Repo              string    `json:"repo"`
	Folder            string    `json:"folder"`
	Enabled           bool      `json:"enabled"`
	SaveChangelog     bool      `json:"saveChangelog"`
	FileTypes         []string  `json:"fileTypes"`
	Regex             string    `json:"regex"`
	IncludePrerelease bool      `json:"includePrerelease"`
	KeepVersions      int       `json:"keepVersions"`
	CreatedAt         Timestamp `json:"createdAt"`
}

func (m Mirror) clone() Mirror {
	c := m
	c.FileTypes = append([]string{}, m.FileTypes...)
	return c
}

type MirrorFile struct {
	Name            string `json:"name"`
	Size            int64  `json:"size"`
	Status          string `json:"status"`
	CompletedLength int64  `json:"completedLength"`
	// Error pending 文件的失败原因（aria2 最近一次失败任务的 errorMessage 等），done/downloading 时为空
	Error string `json:"error,omitempty"`
}

type MirrorState struct {
	Status      string       `json:"status"`
	LatestTag   string       `json:"latestTag"`
	ReleaseName string       `json:"releaseName"`
	ReleaseURL  string       `json:"releaseUrl"`
	PublishedAt Timestamp    `json:"publishedAt"`
	LastCheck   Timestamp    `json:"lastCheck"`
	LastSuccess Timestamp    `json:"lastSuccess"`
	LastError   string       `json:"lastError"`
	Files       []MirrorFile `json:"files"`

	// rev 每次写入状态时递增，用于丢弃并发产生的过期结果（不持久化）
	rev uint64
}

func newIdleState() *MirrorState {
	return &MirrorState{Status: StatusIdle, Files: []MirrorFile{}}
}

func (s *MirrorState) clone() *MirrorState {
	c := *s
	c.Files = append([]MirrorFile{}, s.Files...)
	return &c
}

type mirrorData struct {
	Settings MirrorSettings          `json:"settings"`
	Mirrors  []Mirror                `json:"mirrors"`
	State    map[string]*MirrorState `json:"state"`
}

func defaultMirrorSettings() MirrorSettings {
	return MirrorSettings{RootDir: defaultMirrorRootDir, IntervalMinutes: defaultMirrorInterval}
}

func newMirrorData() *mirrorData {
	return &mirrorData{
		Settings: defaultMirrorSettings(),
		Mirrors:  []Mirror{},
		State:    map[string]*MirrorState{},
	}
}

// normalize 修正从磁盘读取的数据（缺省值、空切片、重复 id、中断的检查状态）
func (d *mirrorData) normalize() {
	if root, err := normalizeRootDir(d.Settings.RootDir); err != nil {
		d.Settings.RootDir = defaultMirrorRootDir
	} else {
		d.Settings.RootDir = root
	}
	if validateInterval(d.Settings.IntervalMinutes) != nil {
		d.Settings.IntervalMinutes = defaultMirrorInterval
	}
	if p, err := normalizeProxy(d.Settings.Proxy); err != nil {
		d.Settings.Proxy = ""
	} else {
		d.Settings.Proxy = p
	}
	if d.Mirrors == nil {
		d.Mirrors = []Mirror{}
	}
	ids := map[string]bool{}
	for i := range d.Mirrors {
		m := &d.Mirrors[i]
		if m.ID == "" || ids[m.ID] {
			m.ID = newMirrorID()
		}
		if m.FileTypes == nil {
			m.FileTypes = []string{}
		}
		if m.KeepVersions < 0 {
			m.KeepVersions = 0
		}
		ids[m.ID] = true
	}
	if d.State == nil {
		d.State = map[string]*MirrorState{}
	}
	for id, st := range d.State {
		if !ids[id] || st == nil {
			delete(d.State, id)
			continue
		}
		if st.Files == nil {
			st.Files = []MirrorFile{}
		}
		switch st.Status {
		case StatusIdle, StatusDownloading, StatusSynced, StatusIncomplete, StatusError:
		case StatusChecking:
			// 上次检查被进程退出打断：交给 RefreshProgress 依据磁盘重新判定
			if st.LatestTag == "" {
				st.Status = StatusIdle
			} else {
				st.Status = StatusDownloading
			}
		default:
			st.Status = StatusIdle
		}
	}
}

func (d *mirrorData) clone() *mirrorData {
	c := &mirrorData{Settings: d.Settings, Mirrors: make([]Mirror, 0, len(d.Mirrors)), State: make(map[string]*MirrorState, len(d.State))}
	for _, m := range d.Mirrors {
		c.Mirrors = append(c.Mirrors, m.clone())
	}
	for id, st := range d.State {
		c.State[id] = st.clone()
	}
	return c
}

func (d *mirrorData) findMirror(id string) int {
	for i := range d.Mirrors {
		if d.Mirrors[i].ID == id {
			return i
		}
	}
	return -1
}

// stateFor 返回镜像状态，不存在时创建 idle 状态
func (d *mirrorData) stateFor(id string) *MirrorState {
	st := d.State[id]
	if st == nil {
		st = newIdleState()
		d.State[id] = st
	}
	return st
}

// stateView 返回镜像状态的副本（只读场景，不修改 map）
func (d *mirrorData) stateView(id string) *MirrorState {
	if st := d.State[id]; st != nil {
		return st.clone()
	}
	return newIdleState()
}

// newUniqueID 生成未被占用的镜像 id
func (d *mirrorData) newUniqueID() string {
	for {
		id := newMirrorID()
		if d.findMirror(id) < 0 {
			return id
		}
	}
}

// MirrorStore 负责 github-mirror.json 的加载与原子持久化
type MirrorStore struct {
	path string
	mu   sync.Mutex
	data *mirrorData
}

// OpenMirrorStore 加载配置；即使返回错误，返回的 store 仍可用（使用默认值）
func OpenMirrorStore(configDir string) (*MirrorStore, error) {
	s := &MirrorStore{path: filepath.Join(configDir, mirrorStoreFile), data: newMirrorData()}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s, nil
		}
		return s, fmt.Errorf("读取 %s 失败: %w", s.path, err)
	}
	d := newMirrorData()
	if err := json.Unmarshal(raw, d); err != nil {
		bak := s.path + ".bad-" + time.Now().Format("20060102-150405")
		if rerr := os.Rename(s.path, bak); rerr != nil {
			return s, fmt.Errorf("解析 %s 失败: %v（备份失败: %v）", s.path, err, rerr)
		}
		return s, fmt.Errorf("解析 %s 失败，已备份为 %s: %w", s.path, bak, err)
	}
	d.normalize()
	s.data = d
	return s, nil
}

// view 在锁内只读访问数据；回调不得做网络 I/O
func (s *MirrorStore) view(fn func(d *mirrorData)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(s.data)
}

// update 在锁内修改并持久化（配置变更）；fn 返回错误或保存失败时回滚内存数据
func (s *MirrorStore) update(fn func(d *mirrorData) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot := s.data.clone()
	if err := fn(s.data); err != nil {
		s.data = snapshot
		return err
	}
	if err := s.saveLocked(); err != nil {
		s.data = snapshot
		return err
	}
	return nil
}

// updateState 用于运行状态写入：fn 返回错误时回滚；persist=false 时只改内存；
// 保存失败仅记录日志，保留内存中的新状态
func (s *MirrorStore) updateState(fn func(d *mirrorData) (persist bool, err error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot := s.data.clone()
	persist, err := fn(s.data)
	if err != nil {
		s.data = snapshot
		return err
	}
	if persist {
		if err := s.saveLocked(); err != nil {
			log.Printf("[mirror] failed to persist state: %v", err)
		}
	}
	return nil
}

// saveLocked 写临时文件 + rename 原子替换，权限 0600（含 Token）
func (s *MirrorStore) saveLocked() error {
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("创建配置目录失败: %w", err)
	}
	f, err := os.CreateTemp(dir, ".github-mirror-*.tmp")
	if err != nil {
		return fmt.Errorf("写入配置失败: %w", err)
	}
	tmp := f.Name()
	committed := false
	defer func() {
		if !committed {
			f.Close()
			os.Remove(tmp)
		}
	}()
	if err := f.Chmod(0600); err != nil {
		return fmt.Errorf("写入配置失败: %w", err)
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("写入配置失败: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("写入配置失败: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("写入配置失败: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		os.Remove(tmp)
		committed = true
		return fmt.Errorf("写入配置失败: %w", err)
	}
	committed = true
	return nil
}

func newMirrorID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand 失败极少见，退化为时间戳
		return fmt.Sprintf("%016x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
