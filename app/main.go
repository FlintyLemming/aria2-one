package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"

	// 内嵌时区数据，保证 TZ 环境变量在精简镜像中也能生效（限流重置时间按本地时区显示）
	_ "time/tzdata"
)

func main() {
	port := flag.String("port", "8081", "Controller listen port")
	aria2URL := flag.String("aria2-url", "http://127.0.0.1:6800/jsonrpc", "Upstream Aria2 JSON-RPC URL")
	downloadDir := flag.String("download-dir", "/downloads", "Aria2 download directory")
	configDir := flag.String("config-dir", "/config", "Directory for github-mirror.json")
	githubAPI := flag.String("github-api", "https://api.github.com", "GitHub API base URL")
	flag.Parse()

	if envPort := os.Getenv("CONTROLLER_PORT"); envPort != "" {
		*port = envPort
	}
	if envAria2 := os.Getenv("ARIA2_RPC_URL"); envAria2 != "" {
		*aria2URL = envAria2
	}
	if envDir := os.Getenv("DOWNLOAD_DIR"); envDir != "" {
		*downloadDir = envDir
	}
	if envCfg := os.Getenv("CONFIG_DIR"); envCfg != "" {
		*configDir = envCfg
	}
	if envAPI := os.Getenv("GITHUB_API_URL"); envAPI != "" {
		*githubAPI = envAPI
	}

	manager, err := NewManager(ManagerConfig{
		DownloadDir: *downloadDir,
		ConfigDir:   *configDir,
		GitHubAPI:   *githubAPI,
		Aria2URL:    *aria2URL,
		EnvToken:    os.Getenv("GITHUB_TOKEN"),
	})
	if err != nil {
		log.Printf("[mirror] load config failed, using defaults: %v", err)
	}

	proxy := NewControllerProxy(*aria2URL, *downloadDir)
	proxy.Protect = manager.IsManagedPath

	mux := http.NewServeMux()
	mux.Handle("/jsonrpc", proxy)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "OK\n")
	})
	RegisterMirrorAPI(mux, manager)

	go manager.Run(context.Background())

	addr := ":" + *port
	log.Printf("[aria2-controller] starting on %s, upstream: %s, downloads: %s, config: %s", addr, *aria2URL, *downloadDir, *configDir)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
