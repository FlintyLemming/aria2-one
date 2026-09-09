package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

type RPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type TellStatusResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id"`
	Result  struct {
		GID   string `json:"gid"`
		Dir   string `json:"dir"`
		Files []struct {
			Path string `json:"path"`
		} `json:"files"`
	} `json:"result"`
	Error interface{} `json:"error"`
}

type ControllerProxy struct {
	upstreamURL  *url.URL
	downloadDir  string
	reverseProxy *httputil.ReverseProxy
	httpClient   *http.Client
}

func NewControllerProxy(upstreamURLStr, downloadDir string) *ControllerProxy {
	u, err := url.Parse(upstreamURLStr)
	if err != nil {
		log.Fatalf("invalid upstream url %s: %v", upstreamURLStr, err)
	}
	rp := httputil.NewSingleHostReverseProxy(u)

	return &ControllerProxy{
		upstreamURL:  u,
		downloadDir:  downloadDir,
		reverseProxy: rp,
		httpClient:   &http.Client{},
	}
}

// PreQueryFiles 根据 gid 查询任务文件路径
func (p *ControllerProxy) PreQueryFiles(gid string, token string) ([]string, error) {
	params := []interface{}{}
	if token != "" {
		params = append(params, token)
	}
	params = append(params, gid, []string{"gid", "dir", "files"})

	reqMap := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      "tellStatus_internal",
		"method":  "aria2.tellStatus",
		"params":  params,
	}
	reqBytes, err := json.Marshal(reqMap)
	if err != nil {
		return nil, err
	}

	resp, err := p.httpClient.Post(p.upstreamURL.String(), "application/json", bytes.NewReader(reqBytes))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var statusResp TellStatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&statusResp); err != nil {
		return nil, err
	}

	var paths []string
	for _, f := range statusResp.Result.Files {
		if f.Path != "" {
			paths = append(paths, f.Path)
		}
	}
	return paths, nil
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

func (p *ControllerProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
		paths, err := p.PreQueryFiles(gid, token)
		if err == nil {
			filesToDelete = paths
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
