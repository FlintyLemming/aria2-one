package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
)

func main() {
	port := flag.String("port", "8081", "Controller listen port")
	aria2URL := flag.String("aria2-url", "http://127.0.0.1:6800/jsonrpc", "Upstream Aria2 JSON-RPC URL")
	downloadDir := flag.String("download-dir", "/downloads", "Aria2 download directory")
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

	proxy := NewControllerProxy(*aria2URL, *downloadDir)

	mux := http.NewServeMux()
	mux.Handle("/jsonrpc", proxy)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "OK\n")
	})

	addr := ":" + *port
	log.Printf("[aria2-controller] starting on %s, upstream: %s, downloads: %s", addr, *aria2URL, *downloadDir)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
