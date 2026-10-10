package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var (
	aria2StatusKeys  = []string{"gid", "status", "files", "completedLength", "totalLength"}
	aria2StoppedKeys = []string{"gid", "status", "files", "errorCode", "errorMessage"}
)

// Aria2Client 是镜像功能使用的最小 aria2 JSON-RPC (HTTP POST) 客户端
type Aria2Client struct {
	URL    string
	Secret string
	HTTP   *http.Client
}

func NewAria2Client(rawURL, secret string) *Aria2Client {
	u := rawURL
	switch {
	case strings.HasPrefix(u, "ws://"):
		u = "http://" + strings.TrimPrefix(u, "ws://")
	case strings.HasPrefix(u, "wss://"):
		u = "https://" + strings.TrimPrefix(u, "wss://")
	}
	return &Aria2Client{URL: u, Secret: secret, HTTP: &http.Client{Timeout: 15 * time.Second}}
}

type aria2Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (c *Aria2Client) call(ctx context.Context, method string, params []interface{}, out interface{}) error {
	if c.Secret != "" {
		params = append([]interface{}{"token:" + c.Secret}, params...)
	}
	if params == nil {
		params = []interface{}{}
	}
	payload, err := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      "aria2-one-mirror",
		"method":  method,
		"params":  params,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("%s 请求失败：%v", method, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return fmt.Errorf("%s 读取响应失败：%v", method, err)
	}
	var rpcResp struct {
		Result json.RawMessage `json:"result"`
		Error  *aria2Error     `json:"error"`
	}
	if err := json.Unmarshal(body, &rpcResp); err != nil {
		return fmt.Errorf("%s 响应无效（HTTP %d）：%v", method, resp.StatusCode, err)
	}
	if rpcResp.Error != nil {
		return fmt.Errorf("%s 失败：%s", method, rpcResp.Error.Message)
	}
	if out != nil {
		if err := json.Unmarshal(rpcResp.Result, out); err != nil {
			return fmt.Errorf("%s 结果无效：%v", method, err)
		}
	}
	return nil
}

// AddURI 投递下载任务，返回 gid
func (c *Aria2Client) AddURI(ctx context.Context, uris []string, options map[string]string) (string, error) {
	var gid string
	if err := c.call(ctx, "aria2.addUri", []interface{}{uris, options}, &gid); err != nil {
		return "", err
	}
	return gid, nil
}

type aria2File struct {
	Path            string `json:"path"`
	Length          string `json:"length"`
	CompletedLength string `json:"completedLength"`
}

type aria2Task struct {
	GID             string      `json:"gid"`
	Status          string      `json:"status"`
	CompletedLength string      `json:"completedLength"`
	TotalLength     string      `json:"totalLength"`
	ErrorCode       string      `json:"errorCode"`
	ErrorMessage    string      `json:"errorMessage"`
	Files           []aria2File `json:"files"`
}

// ActiveTasks 返回 active + waiting(含 paused) 任务（gid、状态、文件路径与进度）
func (c *Aria2Client) ActiveTasks(ctx context.Context) ([]aria2Task, error) {
	var active, waiting []aria2Task
	if err := c.call(ctx, "aria2.tellActive", []interface{}{aria2StatusKeys}, &active); err != nil {
		return nil, err
	}
	if err := c.call(ctx, "aria2.tellWaiting", []interface{}{0, 1000, aria2StatusKeys}, &waiting); err != nil {
		return nil, err
	}
	return append(active, waiting...), nil
}

// InProgress 返回 active + waiting(含 paused) 任务的 文件路径 → 已完成字节 映射
func (c *Aria2Client) InProgress(ctx context.Context) (map[string]int64, error) {
	tasks, err := c.ActiveTasks(ctx)
	if err != nil {
		return nil, err
	}
	result := map[string]int64{}
	for _, t := range tasks {
		for _, f := range t.Files {
			if f.Path == "" {
				continue
			}
			done, err := strconv.ParseInt(f.CompletedLength, 10, 64)
			if err != nil && len(t.Files) == 1 {
				done, _ = strconv.ParseInt(t.CompletedLength, 10, 64)
			}
			result[filepath.Clean(f.Path)] = done
		}
	}
	return result, nil
}

// ForceRemove 直接调用 aria2.forceRemove 移除任务（不经过删除拦截器，不删除文件）
func (c *Aria2Client) ForceRemove(ctx context.Context, gid string) error {
	return c.call(ctx, "aria2.forceRemove", []interface{}{gid}, nil)
}

// StoppedErrors 查询已停止任务，返回 文件路径 → 失败原因；
// 同一路径以最近一次任务为准（tellStopped 按从旧到新排列），最近一次不是 error 时不返回该路径
func (c *Aria2Client) StoppedErrors(ctx context.Context) (map[string]string, error) {
	var stopped []aria2Task
	if err := c.call(ctx, "aria2.tellStopped", []interface{}{0, 1000, aria2StoppedKeys}, &stopped); err != nil {
		return nil, err
	}
	result := map[string]string{}
	for _, t := range stopped {
		msg := strings.TrimSpace(t.ErrorMessage)
		if msg == "" {
			msg = "错误码 " + t.ErrorCode
		}
		for _, f := range t.Files {
			if f.Path == "" {
				continue
			}
			p := filepath.Clean(f.Path)
			if t.Status == "error" {
				result[p] = msg
			} else {
				delete(result, p)
			}
		}
	}
	return result, nil
}
