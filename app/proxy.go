package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

type RPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type ControllerProxy struct {
	upstreamURL  *url.URL
	downloadDir  string
	reverseProxy *httputil.ReverseProxy
	httpClient   *http.Client

	// Protect 返回 true 的路径（如 GitHub Release 镜像文件）不随任务删除
	Protect func(path string) bool
}

var wsUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

func NewControllerProxy(upstreamURLStr, downloadDir string) *ControllerProxy {
	u, err := url.Parse(upstreamURLStr)
	if err != nil {
		log.Fatalf("invalid upstream url %s: %v", upstreamURLStr, err)
	}
	rp := httputil.NewSingleHostReverseProxy(u)
	originalDirector := rp.Director
	rp.Director = func(req *http.Request) {
		originalDirector(req)
		if u.Path != "" && u.Path != "/" {
			req.URL.Path = u.Path
		}
		req.Host = u.Host
	}

	return &ControllerProxy{
		upstreamURL:  u,
		downloadDir:  downloadDir,
		reverseProxy: rp,
		httpClient:   &http.Client{},
	}
}

// RemovableFiles 返回移除 gid 任务时可以一并删除的文件路径。
// 以下文件不删除：
//   - 失败且未写入任何数据的任务：路径上的文件不是它下载的（可能是另一个同名任务的成果）
//   - 仍被其他任务（含已完成/失败的记录）引用的路径
//
// 查询失败时返回错误，调用方应放弃删除
func (p *ControllerProxy) RemovableFiles(gid string, token string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client := NewAria2Client(p.upstreamURL.String(), strings.TrimPrefix(token, "token:"))

	task, err := client.TellStatus(ctx, gid)
	if err != nil {
		return nil, err
	}
	if task.Status == "error" && task.CompletedLength == "0" {
		log.Printf("[cleaner] gid %s failed without writing data, keep files", gid)
		return nil, nil
	}

	all, err := client.AllTasks(ctx)
	if err != nil {
		return nil, err
	}
	shared := map[string]string{}
	for _, t := range all {
		if t.GID == gid {
			continue
		}
		for _, f := range t.Files {
			if f.Path != "" {
				shared[filepath.Clean(f.Path)] = t.GID
			}
		}
	}

	var paths []string
	for _, f := range task.Files {
		if f.Path == "" {
			continue
		}
		if other, ok := shared[filepath.Clean(f.Path)]; ok {
			log.Printf("[cleaner] skip %s: still used by gid %s", f.Path, other)
			continue
		}
		paths = append(paths, f.Path)
	}
	return paths, nil
}

// filterProtected 过滤掉受保护的路径
func (p *ControllerProxy) filterProtected(paths []string) []string {
	if p.Protect == nil || len(paths) == 0 {
		return paths
	}
	kept := make([]string, 0, len(paths))
	for _, path := range paths {
		if p.Protect(path) {
			log.Printf("[cleaner] skip protected mirror file: %s", path)
			continue
		}
		kept = append(kept, path)
	}
	return kept
}

func extractGidAndToken(rawParams json.RawMessage) (string, string) {
	var params []interface{}
	if err := json.Unmarshal(rawParams, &params); err != nil || len(params) == 0 {
		return "", ""
	}

	token := ""
	firstStr, ok := params[0].(string)
	if !ok {
		return "", ""
	}

	if strings.HasPrefix(firstStr, "token:") {
		token = firstStr
		if len(params) > 1 {
			if gid, ok := params[1].(string); ok {
				return gid, token
			}
		}
		return "", token
	}
	return firstStr, ""
}

func (p *ControllerProxy) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	clientConn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[ws] upgrade error: %v", err)
		return
	}
	defer clientConn.Close()

	// 转换 upstream URL 协议至 ws/wss
	u := *p.upstreamURL
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}

	upstreamConn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		log.Printf("[ws] failed to connect to upstream %s: %v", u.String(), err)
		return
	}
	defer upstreamConn.Close()

	errChan := make(chan error, 2)

	// upstream -> client 转发
	go func() {
		for {
			msgType, data, err := upstreamConn.ReadMessage()
			if err != nil {
				errChan <- err
				return
			}
			if err := clientConn.WriteMessage(msgType, data); err != nil {
				errChan <- err
				return
			}
		}
	}()

	// client -> upstream 拦截并转发
	go func() {
		for {
			msgType, data, err := clientConn.ReadMessage()
			if err != nil {
				errChan <- err
				return
			}

			if msgType == websocket.TextMessage {
				var req RPCRequest
				if err := json.Unmarshal(data, &req); err == nil {
					if req.Method == "aria2.remove" || req.Method == "aria2.removeDownloadResult" {
						gid, token := extractGidAndToken(req.Params)
						if gid != "" {
							go func(g, tok string) {
								paths, err := p.RemovableFiles(g, tok)
								if err == nil {
									paths = p.filterProtected(paths)
								} else {
									log.Printf("[cleaner] keep files for gid %s: %v", g, err)
								}
								if err == nil && len(paths) > 0 {
									// 等待 aria2 注销并释放句柄后删除
									time.Sleep(100 * time.Millisecond)
									RemoveTaskFiles(p.downloadDir, paths)
								}
							}(gid, token)
						}
					}
				}
			}

			if err := upstreamConn.WriteMessage(msgType, data); err != nil {
				errChan <- err
				return
			}
		}
	}()

	<-errChan
}

func (p *ControllerProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if websocket.IsWebSocketUpgrade(r) {
		p.handleWebSocket(w, r)
		return
	}

	if r.Method != http.MethodPost {
		p.reverseProxy.ServeHTTP(w, r)
		return
	}

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(bodyBytes))

	// 判断是否为单个请求并拦截删除方法
	var singleReq RPCRequest
	if err := json.Unmarshal(bodyBytes, &singleReq); err == nil && singleReq.Method != "" {
		if singleReq.Method == "aria2.remove" || singleReq.Method == "aria2.removeDownloadResult" {
			p.handleSingleRemove(w, r, bodyBytes, singleReq)
			return
		}
	}

	// 其余请求直接透明反代
	p.reverseProxy.ServeHTTP(w, r)
}

func (p *ControllerProxy) handleSingleRemove(w http.ResponseWriter, r *http.Request, originalBody []byte, req RPCRequest) {
	gid, token := extractGidAndToken(req.Params)
	var filesToDelete []string
	if gid != "" {
		paths, err := p.RemovableFiles(gid, token)
		if err == nil {
			filesToDelete = p.filterProtected(paths)
		} else {
			log.Printf("[cleaner] keep files for gid %s: %v", gid, err)
		}
	}

	// 将原请求转发给 upstream
	reqUpstream, err := http.NewRequest(http.MethodPost, p.upstreamURL.String(), bytes.NewReader(originalBody))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	reqUpstream.Header = r.Header.Clone()

	resp, err := p.httpClient.Do(reqUpstream)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// 任务从 aria2 移除成功后，物理清理磁盘
	if len(filesToDelete) > 0 {
		if deleted, err := RemoveTaskFiles(p.downloadDir, filesToDelete); err == nil {
			log.Printf("[cleaner] successfully removed files for gid %s: %v", gid, deleted)
		} else {
			log.Printf("[cleaner] error removing files for gid %s: %v", gid, err)
		}
	}

	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	w.Write(respBody)
}
