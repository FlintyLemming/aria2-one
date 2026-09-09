package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// 模拟 upstream Aria2 RPC 服务器
type mockAria2Server struct {
	tasks map[string][]string // gid -> files
}

func (m *mockAria2Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req map[string]interface{}
	json.Unmarshal(body, &req)

	method, _ := req["method"].(string)
	id := req["id"]

	switch method {
	case "aria2.getVersion":
		resp := map[string]interface{}{
			"id":      id,
			"jsonrpc": "2.0",
			"result":  map[string]interface{}{"version": "1.37.0"},
		}
		json.NewEncoder(w).Encode(resp)
	case "aria2.tellStatus":
		params := req["params"].([]interface{})
		gid := params[0].(string)
		files := []map[string]interface{}{}
		for _, f := range m.tasks[gid] {
			files = append(files, map[string]interface{}{"path": f})
		}
		resp := map[string]interface{}{
			"id":      id,
			"jsonrpc": "2.0",
			"result": map[string]interface{}{
				"gid":   gid,
				"dir":   "/downloads",
				"files": files,
			},
		}
		json.NewEncoder(w).Encode(resp)
	case "aria2.remove", "aria2.removeDownloadResult":
		params := req["params"].([]interface{})
		gid := params[0].(string)
		delete(m.tasks, gid)
		resp := map[string]interface{}{
			"id":      id,
			"jsonrpc": "2.0",
			"result":  gid,
		}
		json.NewEncoder(w).Encode(resp)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestProxyTransparentAndIntercept(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "proxy-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	testFile := filepath.Join(tmpDir, "test.iso")
	if err := os.WriteFile(testFile, []byte("data"), 0644); err != nil {
		t.Fatalf("failed to create file: %v", err)
	}

	mock := &mockAria2Server{
		tasks: map[string][]string{
			"gid123": {testFile},
		},
	}
	mockServer := httptest.NewServer(mock)
	defer mockServer.Close()

	proxy := NewControllerProxy(mockServer.URL, tmpDir)

	// 1. 测试透明代理 getVersion
	reqBody := `{"jsonrpc":"2.0","method":"aria2.getVersion","id":"1","params":[]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/jsonrpc", bytes.NewBufferString(reqBody))
	proxy.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("1.37.0")) {
		t.Errorf("expected version 1.37.0, got %s", rec.Body.String())
	}

	// 2. 测试拦截 aria2.removeDownloadResult 并物理删除文件
	delReqBody := `{"jsonrpc":"2.0","method":"aria2.removeDownloadResult","id":"2","params":["gid123"]}`
	recDel := httptest.NewRecorder()
	reqDel := httptest.NewRequest("POST", "/jsonrpc", bytes.NewBufferString(delReqBody))
	proxy.ServeHTTP(recDel, reqDel)

	if recDel.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recDel.Code)
	}
	if !bytes.Contains(recDel.Body.Bytes(), []byte("gid123")) {
		t.Errorf("expected gid123 in response, got %s", recDel.Body.String())
	}

	// 确认物理文件已被删除
	if _, err := os.Stat(testFile); !os.IsNotExist(err) {
		t.Errorf("expected testFile to be physically deleted, but it still exists")
	}
}
