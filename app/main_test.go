package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

func TestWebSocketRemoveInterception(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ws-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	testFile := filepath.Join(tmpDir, "ws_video.mp4")
	if err := os.WriteFile(testFile, []byte("video data"), 0644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}

	// 模拟 upstream 支持 WebSocket 的 aria2
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if websocket.IsWebSocketUpgrade(r) {
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer conn.Close()
			for {
				_, msg, err := conn.ReadMessage()
				if err != nil {
					break
				}
				var req map[string]interface{}
				json.Unmarshal(msg, &req)
				method, _ := req["method"].(string)
				id := req["id"]
				if method == "aria2.tellStatus" {
					resp := map[string]interface{}{
						"jsonrpc": "2.0",
						"id":      id,
						"result": map[string]interface{}{
							"gid":   "gid_ws_1",
							"dir":   tmpDir,
							"files": []map[string]interface{}{{"path": testFile}},
						},
					}
					b, _ := json.Marshal(resp)
					conn.WriteMessage(websocket.TextMessage, b)
				} else if method == "aria2.removeDownloadResult" {
					resp := map[string]interface{}{
						"jsonrpc": "2.0",
						"id":      id,
						"result":  "gid_ws_1",
					}
					b, _ := json.Marshal(resp)
					conn.WriteMessage(websocket.TextMessage, b)
				}
			}
		} else {
			// 普通 HTTP tellStatus 处理，其余任务列表为空
			var req map[string]interface{}
			json.NewDecoder(r.Body).Decode(&req)
			if req["method"] != "aria2.tellStatus" {
				json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": req["id"], "result": []interface{}{}})
				return
			}
			resp := map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      req["id"],
				"result": map[string]interface{}{
					"gid":   "gid_ws_1",
					"dir":   tmpDir,
					"files": []map[string]interface{}{{"path": testFile}},
				},
			}
			json.NewEncoder(w).Encode(resp)
		}
	}))
	defer upstreamServer.Close()

	proxy := NewControllerProxy(upstreamServer.URL, tmpDir)
	proxyServer := httptest.NewServer(proxy)
	defer proxyServer.Close()

	wsURL := "ws" + strings.TrimPrefix(proxyServer.URL, "http") + "/jsonrpc"
	clientConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("failed to dial websocket proxy: %v", err)
	}
	defer clientConn.Close()

	// 发送 removeDownloadResult
	removeMsg := `{"jsonrpc":"2.0","method":"aria2.removeDownloadResult","id":"ws-1","params":["gid_ws_1"]}`
	if err := clientConn.WriteMessage(websocket.TextMessage, []byte(removeMsg)); err != nil {
		t.Fatalf("failed to write ws message: %v", err)
	}

	clientConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, respMsg, err := clientConn.ReadMessage()
	if err != nil {
		t.Fatalf("failed to read ws response: %v", err)
	}

	if !strings.Contains(string(respMsg), "gid_ws_1") {
		t.Errorf("expected gid_ws_1 in ws response, got: %s", string(respMsg))
	}

	// 验证文件是否已删除 (轮询等待，避免协程调度时延导致误判)
	var deleted bool
	for i := 0; i < 20; i++ {
		if _, err := os.Stat(testFile); os.IsNotExist(err) {
			deleted = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !deleted {
		t.Errorf("expected %s to be deleted via WS interception", testFile)
	}
}

func TestWebSocketRemoveSkipsProtectedFiles(t *testing.T) {
	tmpDir := t.TempDir()
	protectedFile := filepath.Join(tmpDir, "github-mirror", "App", "v1", "app.exe")
	plainFile := filepath.Join(tmpDir, "ws_plain.mp4")
	for _, f := range []string{protectedFile, plainFile} {
		if err := os.MkdirAll(filepath.Dir(f), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte("data"), 0644); err != nil {
			t.Fatalf("failed to write file: %v", err)
		}
	}

	tellStatus := func(id interface{}) []byte {
		b, _ := json.Marshal(map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      id,
			"result": map[string]interface{}{
				"gid":   "gid_ws_2",
				"dir":   tmpDir,
				"files": []map[string]interface{}{{"path": protectedFile}, {"path": plainFile}},
			},
		})
		return b
	}
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if websocket.IsWebSocketUpgrade(r) {
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer conn.Close()
			for {
				_, msg, err := conn.ReadMessage()
				if err != nil {
					break
				}
				var req map[string]interface{}
				json.Unmarshal(msg, &req)
				resp, _ := json.Marshal(map[string]interface{}{"jsonrpc": "2.0", "id": req["id"], "result": "gid_ws_2"})
				conn.WriteMessage(websocket.TextMessage, resp)
			}
			return
		}
		var req map[string]interface{}
		json.NewDecoder(r.Body).Decode(&req)
		if req["method"] != "aria2.tellStatus" {
			json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": req["id"], "result": []interface{}{}})
			return
		}
		w.Write(tellStatus(req["id"]))
	}))
	defer upstreamServer.Close()

	proxy := NewControllerProxy(upstreamServer.URL, tmpDir)
	proxy.Protect = func(p string) bool {
		_, ok := IsSafeSubpath(filepath.Join(tmpDir, "github-mirror"), p)
		return ok
	}
	proxyServer := httptest.NewServer(proxy)
	defer proxyServer.Close()

	wsURL := "ws" + strings.TrimPrefix(proxyServer.URL, "http") + "/jsonrpc"
	clientConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("failed to dial websocket proxy: %v", err)
	}
	defer clientConn.Close()

	removeMsg := `{"jsonrpc":"2.0","method":"aria2.removeDownloadResult","id":"ws-2","params":["gid_ws_2"]}`
	if err := clientConn.WriteMessage(websocket.TextMessage, []byte(removeMsg)); err != nil {
		t.Fatalf("failed to write ws message: %v", err)
	}
	clientConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := clientConn.ReadMessage(); err != nil {
		t.Fatalf("failed to read ws response: %v", err)
	}

	// 未受保护的文件被删除后，再确认受保护文件仍在
	var deleted bool
	for i := 0; i < 40; i++ {
		if _, err := os.Stat(plainFile); os.IsNotExist(err) {
			deleted = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !deleted {
		t.Fatalf("expected %s to be deleted via WS interception", plainFile)
	}
	if _, err := os.Stat(protectedFile); err != nil {
		t.Errorf("protected mirror file was deleted via WS interception: %v", err)
	}
}

func TestProxyProtectWithManager(t *testing.T) {
	env := newTestEnv(t, nil)
	mirrorFile := filepath.Join(env.m.downloadDir, "github-mirror", "App", "v1", "a.exe")
	writeTestFile(t, mirrorFile, 4)

	upstream := httptest.NewServer(&mockAria2Server{tasks: map[string][]string{"g": {mirrorFile}}})
	defer upstream.Close()
	proxy := NewControllerProxy(upstream.URL, env.m.downloadDir)
	proxy.Protect = env.m.IsManagedPath

	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, httptest.NewRequest("POST", "/jsonrpc", strings.NewReader(`{"jsonrpc":"2.0","method":"aria2.removeDownloadResult","id":"1","params":["g"]}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if _, err := os.Stat(mirrorFile); err != nil {
		t.Errorf("mirror file under rootDir must survive task removal: %v", err)
	}
}
