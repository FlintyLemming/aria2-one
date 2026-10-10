package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func boolPtr(b bool) *bool { return &b }

func strPtr(s string) *string { return &s }

// testClock 可控时钟
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func newTestClock() *testClock {
	return &testClock{t: time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC)}
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// ---- mock GitHub API ----

type ghResponse struct {
	status int
	header map[string]string
	body   string
}

type ghRequest struct {
	path       string
	query      string
	requestURI string
	header     http.Header
}

type fakeGitHub struct {
	mu       sync.Mutex
	routes   map[string]ghResponse
	requests []ghRequest
	block    chan struct{}
	srv      *httptest.Server
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	f := &fakeGitHub{routes: map[string]ghResponse{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGitHub) serve(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Path
	if r.URL.RawQuery != "" {
		key += "?" + r.URL.RawQuery
	}
	f.mu.Lock()
	f.requests = append(f.requests, ghRequest{path: r.URL.Path, query: r.URL.RawQuery, requestURI: r.RequestURI, header: r.Header.Clone()})
	resp, ok := f.routes[key]
	block := f.block
	f.mu.Unlock()
	if block != nil {
		<-block
	}
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"message":"Not Found"}`)
		return
	}
	for k, v := range resp.header {
		w.Header().Set(k, v)
	}
	w.Header().Set("Content-Type", "application/json")
	status := resp.status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	io.WriteString(w, resp.body)
}

func (f *fakeGitHub) set(path string, status int, body string, header map[string]string) {
	f.mu.Lock()
	f.routes[path] = ghResponse{status: status, body: body, header: header}
	f.mu.Unlock()
}

func (f *fakeGitHub) setJSON(path string, v interface{}) {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	f.set(path, http.StatusOK, string(b), nil)
}

func (f *fakeGitHub) setBlock(ch chan struct{}) {
	f.mu.Lock()
	f.block = ch
	f.mu.Unlock()
}

func (f *fakeGitHub) requestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func (f *fakeGitHub) requestsFor(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.requests {
		if r.path == path {
			n++
		}
	}
	return n
}

func (f *fakeGitHub) lastRequest() ghRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		return ghRequest{}
	}
	return f.requests[len(f.requests)-1]
}

type testAsset struct {
	name string
	size int64
}

func testRelease(tag, body, dlBase string, assets ...testAsset) map[string]interface{} {
	list := []map[string]interface{}{}
	for _, a := range assets {
		list = append(list, map[string]interface{}{
			"name":                 a.name,
			"size":                 a.size,
			"browser_download_url": dlBase + "/" + a.name,
		})
	}
	return map[string]interface{}{
		"tag_name":     tag,
		"name":         "Release " + tag,
		"body":         body,
		"html_url":     "https://github.com/o/r/releases/tag/" + tag,
		"draft":        false,
		"prerelease":   false,
		"published_at": "2026-10-01T12:00:00Z",
		"assets":       list,
	}
}

// ---- mock aria2 JSON-RPC ----

type fakeTask struct {
	path      string
	completed int64
	total     int64
	gid       string // 为空时自动生成
}

// fakeStoppedTask tellStopped 返回的已停止任务（按从旧到新排列）
type fakeStoppedTask struct {
	gid, status, path, errorCode, errorMessage string
}

type addURICall struct {
	uris    []string
	options map[string]string
}

type fakeAria2 struct {
	mu          sync.Mutex
	active      []fakeTask
	waiting     []fakeTask
	stopped     []fakeStoppedTask
	addCalls    []addURICall
	addErr      string
	trackAdded  bool // addUri 成功后把任务放进 waiting 列表
	tokens      []string
	waitingArgs [][]json.RawMessage
	stoppedArgs [][]json.RawMessage
	removed     []string // forceRemove 的 gid
	srv         *httptest.Server
}

func newFakeAria2(t *testing.T) *fakeAria2 {
	f := &fakeAria2{}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func taskJSON(gid, status string, task fakeTask) map[string]interface{} {
	if task.gid != "" {
		gid = task.gid
	}
	c := strconv.FormatInt(task.completed, 10)
	tl := strconv.FormatInt(task.total, 10)
	return map[string]interface{}{
		"gid":             gid,
		"status":          status,
		"completedLength": c,
		"totalLength":     tl,
		"files": []map[string]interface{}{
			{"index": "1", "path": task.path, "length": tl, "completedLength": c, "selected": "true"},
		},
	}
}

func (f *fakeAria2) serve(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID     interface{}       `json:"id"`
		Method string            `json:"method"`
		Params []json.RawMessage `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	params := req.Params
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(params) > 0 {
		var s string
		if json.Unmarshal(params[0], &s) == nil && strings.HasPrefix(s, "token:") {
			f.tokens = append(f.tokens, s)
			params = params[1:]
		}
	}
	resp := map[string]interface{}{"jsonrpc": "2.0", "id": req.ID}
	switch req.Method {
	case "aria2.addUri":
		var call addURICall
		json.Unmarshal(params[0], &call.uris)
		json.Unmarshal(params[1], &call.options)
		f.addCalls = append(f.addCalls, call)
		if f.addErr != "" {
			resp["error"] = map[string]interface{}{"code": 1, "message": f.addErr}
			break
		}
		gid := "gid" + strconv.Itoa(len(f.addCalls))
		if f.trackAdded {
			f.waiting = append(f.waiting, fakeTask{path: call.options["dir"] + "/" + call.options["out"]})
		}
		resp["result"] = gid
	case "aria2.tellActive":
		list := []interface{}{}
		for i, t := range f.active {
			list = append(list, taskJSON("a"+strconv.Itoa(i), "active", t))
		}
		resp["result"] = list
	case "aria2.tellWaiting":
		f.waitingArgs = append(f.waitingArgs, params)
		list := []interface{}{}
		for i, t := range f.waiting {
			list = append(list, taskJSON("w"+strconv.Itoa(i), "paused", t))
		}
		resp["result"] = list
	case "aria2.tellStopped":
		f.stoppedArgs = append(f.stoppedArgs, params)
		list := []interface{}{}
		for _, t := range f.stopped {
			list = append(list, map[string]interface{}{
				"gid":          t.gid,
				"status":       t.status,
				"errorCode":    t.errorCode,
				"errorMessage": t.errorMessage,
				"files":        []map[string]interface{}{{"index": "1", "path": t.path}},
			})
		}
		resp["result"] = list
	case "aria2.forceRemove":
		var gid string
		json.Unmarshal(params[0], &gid)
		found := false
		filter := func(tasks []fakeTask, prefix string) []fakeTask {
			var kept []fakeTask
			for i, t := range tasks {
				id := t.gid
				if id == "" {
					id = prefix + strconv.Itoa(i)
				}
				if id == gid {
					found = true
					continue
				}
				kept = append(kept, t)
			}
			return kept
		}
		f.active = filter(f.active, "a")
		f.waiting = filter(f.waiting, "w")
		if !found {
			resp["error"] = map[string]interface{}{"code": 1, "message": "GID " + gid + " is not found"}
			break
		}
		f.removed = append(f.removed, gid)
		resp["result"] = gid
	default:
		resp["error"] = map[string]interface{}{"code": 1, "message": "Method not found"}
	}
	json.NewEncoder(w).Encode(resp)
}

func (f *fakeAria2) setActive(tasks ...fakeTask) {
	f.mu.Lock()
	f.active = tasks
	f.mu.Unlock()
}

func (f *fakeAria2) setWaiting(tasks ...fakeTask) {
	f.mu.Lock()
	f.waiting = tasks
	f.mu.Unlock()
}

func (f *fakeAria2) setStopped(tasks ...fakeStoppedTask) {
	f.mu.Lock()
	f.stopped = tasks
	f.mu.Unlock()
}

func (f *fakeAria2) stoppedCalls() [][]json.RawMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]json.RawMessage{}, f.stoppedArgs...)
}

func (f *fakeAria2) removedGIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.removed...)
}

func (f *fakeAria2) calls() []addURICall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]addURICall{}, f.addCalls...)
}

func (f *fakeAria2) setTrackAdded(v bool) {
	f.mu.Lock()
	f.trackAdded = v
	f.mu.Unlock()
}

func (f *fakeAria2) setAddErr(msg string) {
	f.mu.Lock()
	f.addErr = msg
	f.mu.Unlock()
}

// ---- 测试管理器 ----

type testEnv struct {
	m     *Manager
	gh    *fakeGitHub
	a2    *fakeAria2
	clock *testClock
}

func newTestEnv(t *testing.T, mutate func(*ManagerConfig)) *testEnv {
	t.Helper()
	gh := newFakeGitHub(t)
	a2 := newFakeAria2(t)
	clock := newTestClock()
	cfg := ManagerConfig{
		DownloadDir:   t.TempDir(),
		ConfigDir:     t.TempDir(),
		GitHubAPI:     gh.srv.URL,
		Aria2URL:      a2.srv.URL,
		GitHubTimeout: 5 * time.Second,
		Location:      time.UTC,
		Now:           clock.Now,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	m, err := NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(m.Close)
	return &testEnv{m: m, gh: gh, a2: a2, clock: clock}
}

// addMirror 直接创建（默认禁用，避免触发异步同步）
func (e *testEnv) addMirror(t *testing.T, in MirrorInput) MirrorView {
	t.Helper()
	if in.Enabled == nil {
		in.Enabled = boolPtr(false)
	}
	v, err := e.m.CreateMirror(in)
	if err != nil {
		t.Fatalf("CreateMirror(%+v): %v", in, err)
	}
	return v
}

func (e *testEnv) state(id string) *MirrorState {
	var st *MirrorState
	e.m.store.view(func(d *mirrorData) { st = d.stateView(id) })
	return st
}

func (e *testEnv) folderDir(folder string) string {
	return filepath.Join(e.m.downloadDir, "github-mirror", folder)
}

func writeTestFile(t *testing.T, p string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(strings.Repeat("x", size)), 0644); err != nil {
		t.Fatal(err)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
