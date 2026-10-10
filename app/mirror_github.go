package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	githubAPIVersion   = "2022-11-28"
	githubUserAgent    = "aria2-one"
	githubPrereleaseN  = 10
	githubMaxBodyBytes = 32 << 20
)

type GitHubAsset struct {
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type GitHubRelease struct {
	TagName     string        `json:"tag_name"`
	Name        string        `json:"name"`
	Body        string        `json:"body"`
	HTMLURL     string        `json:"html_url"`
	Draft       bool          `json:"draft"`
	Prerelease  bool          `json:"prerelease"`
	PublishedAt string        `json:"published_at"`
	Assets      []GitHubAsset `json:"assets"`
}

// PublishedTime 解析发布时间，格式异常时返回零值（不影响同步）
func (r *GitHubRelease) PublishedTime() Timestamp {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(r.PublishedAt))
	if err != nil {
		return Timestamp{}
	}
	return Timestamp{Time: t}
}

// GitHubClient 请求 GitHub Releases API；代理只取自设置，不读取环境变量
type GitHubClient struct {
	BaseURL  string
	Token    string
	Proxy    string
	Timeout  time.Duration
	Location *time.Location
}

func (c *GitHubClient) httpClient() (*http.Client, *http.Transport, error) {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	if c.Proxy != "" {
		pu, err := url.Parse(c.Proxy)
		if err != nil {
			return nil, nil, fmt.Errorf("代理地址无效：%v", err)
		}
		tr.Proxy = http.ProxyURL(pu)
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &http.Client{Transport: tr, Timeout: timeout}, tr, nil
}

// LatestRelease 获取最新 Release：includePrerelease=false 用 /releases/latest，否则取列表中第一个非草稿
func (c *GitHubClient) LatestRelease(ctx context.Context, repo string, includePrerelease bool) (*GitHubRelease, error) {
	if !includePrerelease {
		var rel GitHubRelease
		if err := c.get(ctx, "/repos/"+repo+"/releases/latest", "仓库不存在或没有正式 Release", &rel); err != nil {
			return nil, err
		}
		if rel.TagName == "" {
			return nil, errors.New("GitHub API 返回的 Release 缺少 tag_name")
		}
		return &rel, nil
	}
	var list []GitHubRelease
	path := "/repos/" + repo + "/releases?per_page=" + strconv.Itoa(githubPrereleaseN)
	if err := c.get(ctx, path, "仓库不存在或没有 Release", &list); err != nil {
		return nil, err
	}
	for i := range list {
		if !list[i].Draft && list[i].TagName != "" {
			return &list[i], nil
		}
	}
	return nil, errors.New("该仓库没有已发布的 Release")
}

func (c *GitHubClient) get(ctx context.Context, path, notFoundMsg string, out interface{}) error {
	client, tr, err := c.httpClient()
	if err != nil {
		return err
	}
	defer tr.CloseIdleConnections()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.BaseURL, "/")+path, nil)
	if err != nil {
		return fmt.Errorf("GitHub API 地址无效：%v", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", githubAPIVersion)
	req.Header.Set("User-Agent", githubUserAgent)
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("请求 GitHub API 失败：%v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, githubMaxBodyBytes))
	if err != nil {
		return fmt.Errorf("读取 GitHub API 响应失败：%v", err)
	}

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return errors.New(notFoundMsg)
	case (resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests) &&
		strings.TrimSpace(resp.Header.Get("X-RateLimit-Remaining")) == "0":
		return errors.New(c.rateLimitMessage(resp.Header.Get("X-RateLimit-Reset")))
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return fmt.Errorf("GitHub API 返回 %d：%s", resp.StatusCode, githubErrorMessage(resp.StatusCode, body))
	}

	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("解析 GitHub API 响应失败：%v", err)
	}
	return nil
}

func (c *GitHubClient) rateLimitMessage(resetHeader string) string {
	msg := "GitHub API 限流"
	if reset, err := strconv.ParseInt(strings.TrimSpace(resetHeader), 10, 64); err == nil && reset > 0 {
		loc := c.Location
		if loc == nil {
			loc = time.Local
		}
		msg += "，将于 " + time.Unix(reset, 0).In(loc).Format("15:04") + " 重置"
	}
	return msg + "，建议在设置中填写 Token"
}

func githubErrorMessage(code int, body []byte) string {
	var payload struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &payload); err == nil && strings.TrimSpace(payload.Message) != "" {
		return strings.TrimSpace(payload.Message)
	}
	text := strings.TrimSpace(string(body))
	if r := []rune(text); len(r) > 200 {
		text = string(r[:200]) + "…"
	}
	if text == "" {
		text = http.StatusText(code)
	}
	return text
}
