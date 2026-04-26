package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
)

func main() {
	configPath := flag.String("config", "/etc/egh-node/config.yml", "Path to config file")
	flag.Parse()

	cfg, err := LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/system", systemHandler)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	addr := fmt.Sprintf("%s:%d", cfg.API.Host, cfg.API.Port)

	log.Printf("starting egh-node on %s", addr)
	log.Printf("remote panel: %s", cfg.Remote)
	log.Printf("node id: %d", cfg.NodeID)

	go heartbeatLoop(cfg, fmt.Sprintf("%d", cfg.NodeID))

	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Printf("server stopped: %v", err)
		os.Exit(1)
	}
}
