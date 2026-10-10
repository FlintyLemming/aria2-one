package main

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestGitHubClientLatestReleaseHeaders(t *testing.T) {
	gh := newFakeGitHub(t)
	gh.setJSON("/repos/o/r/releases/latest", testRelease("v1.2.3", "notes", "https://dl", testAsset{"a.exe", 1}))

	c := &GitHubClient{BaseURL: gh.srv.URL + "/", Token: "tok123", Location: time.UTC}
	rel, err := c.LatestRelease(context.Background(), "o/r", false)
	if err != nil {
		t.Fatal(err)
	}
	if rel.TagName != "v1.2.3" || rel.Body != "notes" || len(rel.Assets) != 1 || rel.Assets[0].BrowserDownloadURL != "https://dl/a.exe" {
		t.Errorf("unexpected release: %+v", rel)
	}
	if !rel.PublishedTime().Equal(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("published time = %v", rel.PublishedTime())
	}
	req := gh.lastRequest()
	if req.path != "/repos/o/r/releases/latest" {
		t.Errorf("path = %q", req.path)
	}
	want := map[string]string{
		"Accept":               "application/vnd.github+json",
		"X-Github-Api-Version": "2022-11-28",
		"User-Agent":           "aria2-one",
		"Authorization":        "Bearer tok123",
	}
	for k, v := range want {
		if got := req.header.Get(k); got != v {
			t.Errorf("header %s = %q, want %q", k, got, v)
		}
	}

	c.Token = ""
	if _, err := c.LatestRelease(context.Background(), "o/r", false); err != nil {
		t.Fatal(err)
	}
	if auth := gh.lastRequest().header.Get("Authorization"); auth != "" {
		t.Errorf("Authorization should be absent without token, got %q", auth)
	}
}

func TestGitHubClientBasePathPrefix(t *testing.T) {
	gh := newFakeGitHub(t)
	gh.setJSON("/download/fake-api/repos/o/r/releases/latest", testRelease("v1", "", "x"))
	c := &GitHubClient{BaseURL: gh.srv.URL + "/download/fake-api"}
	if _, err := c.LatestRelease(context.Background(), "o/r", false); err != nil {
		t.Fatalf("base URL with path prefix failed: %v", err)
	}
}

func TestGitHubClientPrereleaseSkipsDrafts(t *testing.T) {
	gh := newFakeGitHub(t)
	draft := testRelease("v3.0.0", "", "x")
	draft["draft"] = true
	pre := testRelease("v2.0.0-rc1", "", "x")
	pre["prerelease"] = true
	stable := testRelease("v1.0.0", "", "x")
	gh.setJSON("/repos/o/r/releases?per_page=10", []interface{}{draft, pre, stable})

	c := &GitHubClient{BaseURL: gh.srv.URL}
	rel, err := c.LatestRelease(context.Background(), "o/r", true)
	if err != nil {
		t.Fatal(err)
	}
	if rel.TagName != "v2.0.0-rc1" {
		t.Errorf("tag = %q, want first non-draft v2.0.0-rc1", rel.TagName)
	}
	if req := gh.lastRequest(); req.path != "/repos/o/r/releases" || req.query != "per_page=10" {
		t.Errorf("request = %s?%s", req.path, req.query)
	}

	gh.setJSON("/repos/o/r/releases?per_page=10", []interface{}{draft})
	if _, err := c.LatestRelease(context.Background(), "o/r", true); err == nil || !strings.Contains(err.Error(), "没有已发布的 Release") {
		t.Errorf("all drafts error = %v", err)
	}
}

func TestGitHubClientErrors(t *testing.T) {
	reset := time.Date(2026, 10, 8, 11, 23, 0, 0, time.UTC)
	cases := []struct {
		name   string
		status int
		body   string
		header map[string]string
		want   string
	}{
		{"not found", 404, `{"message":"Not Found"}`, nil, "仓库不存在或没有正式 Release"},
		{"rate limit 403", 403, `{"message":"API rate limit exceeded"}`,
			map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": strconv.FormatInt(reset.Unix(), 10)},
			"GitHub API 限流，将于 11:23 重置，建议在设置中填写 Token"},
		{"rate limit 429", 429, `{}`,
			map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": strconv.FormatInt(reset.Unix(), 10)},
			"GitHub API 限流，将于 11:23 重置，建议在设置中填写 Token"},
		{"rate limit without reset", 403, `{}`, map[string]string{"X-RateLimit-Remaining": "0"}, "GitHub API 限流，建议在设置中填写 Token"},
		{"forbidden other", 403, `{"message":"Resource not accessible"}`, map[string]string{"X-RateLimit-Remaining": "42"}, "GitHub API 返回 403：Resource not accessible"},
		{"server error", 502, `<html>bad gateway</html>`, nil, "GitHub API 返回 502：<html>bad gateway</html>"},
		{"empty body", 500, ``, nil, "GitHub API 返回 500：Internal Server Error"},
		{"bad json", 200, `{not json`, nil, "解析 GitHub API 响应失败"},
		{"missing tag", 200, `{"name":"x"}`, nil, "缺少 tag_name"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gh := newFakeGitHub(t)
			gh.set("/repos/o/r/releases/latest", c.status, c.body, c.header)
			client := &GitHubClient{BaseURL: gh.srv.URL, Location: time.UTC}
			_, err := client.LatestRelease(context.Background(), "o/r", false)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("error = %v, want containing %q", err, c.want)
			}
		})
	}

	// 时区：按配置的本地时区格式化 HH:MM
	gh := newFakeGitHub(t)
	gh.set("/repos/o/r/releases/latest", 403, `{}`, map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": strconv.FormatInt(reset.Unix(), 10)})
	client := &GitHubClient{BaseURL: gh.srv.URL, Location: time.FixedZone("CST", 8*3600)}
	if _, err := client.LatestRelease(context.Background(), "o/r", false); err == nil || !strings.Contains(err.Error(), "将于 19:23 重置") {
		t.Errorf("timezone formatting error = %v", err)
	}
}

func TestGitHubClientProxyOnlyFromSettings(t *testing.T) {
	c := &GitHubClient{BaseURL: "http://unused"}
	_, tr, err := c.httpClient()
	if err != nil {
		t.Fatal(err)
	}
	if tr.Proxy != nil {
		t.Error("without a configured proxy the transport must not use environment proxies")
	}

	// 把 mock GitHub 当作 HTTP 代理：请求只有经过代理才能成功
	gh := newFakeGitHub(t)
	gh.setJSON("/repos/o/r/releases/latest", testRelease("v9", "", "x"))
	c = &GitHubClient{BaseURL: "http://api.github.invalid", Proxy: gh.srv.URL}
	rel, err := c.LatestRelease(context.Background(), "o/r", false)
	if err != nil {
		t.Fatalf("request through proxy failed: %v", err)
	}
	if rel.TagName != "v9" {
		t.Errorf("tag = %q", rel.TagName)
	}
	if uri := gh.lastRequest().requestURI; uri != "http://api.github.invalid/repos/o/r/releases/latest" {
		t.Errorf("proxy received request URI %q, want absolute form", uri)
	}
}

func TestGitHubClientTimeout(t *testing.T) {
	gh := newFakeGitHub(t)
	block := make(chan struct{})
	gh.setBlock(block)
	defer close(block)
	c := &GitHubClient{BaseURL: gh.srv.URL, Timeout: 100 * time.Millisecond}
	start := time.Now()
	_, err := c.LatestRelease(context.Background(), "o/r", false)
	if err == nil || !strings.Contains(err.Error(), "请求 GitHub API 失败") {
		t.Errorf("timeout error = %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("timeout not applied")
	}
}

func TestAria2ClientInProgressAndSecret(t *testing.T) {
	a2 := newFakeAria2(t)
	a2.setActive(fakeTask{path: "/downloads/x/a.bin", completed: 5, total: 10})
	a2.setWaiting(fakeTask{path: "/downloads/x/../x/b.bin", completed: 1, total: 2}, fakeTask{path: ""})

	c := NewAria2Client(a2.srv.URL, "s3cret")
	got, err := c.InProgress(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["/downloads/x/a.bin"] != 5 || got["/downloads/x/b.bin"] != 1 {
		t.Errorf("InProgress = %v", got)
	}
	a2.mu.Lock()
	tokens := append([]string{}, a2.tokens...)
	args := a2.waitingArgs
	a2.mu.Unlock()
	if len(tokens) != 2 || tokens[0] != "token:s3cret" {
		t.Errorf("secret not sent: %v", tokens)
	}
	if len(args) != 1 || string(args[0][0]) != "0" || string(args[0][1]) != "1000" {
		t.Errorf("tellWaiting args = %s", args)
	}
	var keys []string
	json.Unmarshal(args[0][2], &keys)
	if strings.Join(keys, ",") != "gid,status,files,completedLength,totalLength" {
		t.Errorf("tellWaiting keys = %v", keys)
	}

	gid, err := c.AddURI(context.Background(), []string{"http://x/a"}, map[string]string{"dir": "/d", "out": "a"})
	if err != nil || gid == "" {
		t.Errorf("AddURI = %q, %v", gid, err)
	}
	a2.setAddErr("boom")
	if _, err := c.AddURI(context.Background(), []string{"http://x/a"}, nil); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("AddURI error = %v", err)
	}

	if ws := NewAria2Client("ws://127.0.0.1:6800/jsonrpc", ""); ws.URL != "http://127.0.0.1:6800/jsonrpc" {
		t.Errorf("ws url not converted: %s", ws.URL)
	}
	if wss := NewAria2Client("wss://h/jsonrpc", ""); wss.URL != "https://h/jsonrpc" {
		t.Errorf("wss url not converted: %s", wss.URL)
	}

	dead := NewAria2Client("http://127.0.0.1:1/jsonrpc", "")
	dead.HTTP.Timeout = time.Second
	if _, err := dead.InProgress(context.Background()); err == nil {
		t.Error("expected connection error")
	}
}
