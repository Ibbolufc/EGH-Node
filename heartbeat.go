package main

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

func heartbeatLoop(cfg *Config, nodeID string) {
	if nodeID == "" {
		log.Println("heartbeat disabled: nodeID not set")
		return
	}

	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	url := fmt.Sprintf("%s/api/nodes/%s/heartbeat", strings.TrimRight(cfg.Remote, "/"), nodeID)

	for {
		req, err := http.NewRequest(http.MethodPost, url, bytes.NewBuffer([]byte("{}")))
		if err != nil {
			log.Printf("heartbeat request build failed: %v", err)
			time.Sleep(30 * time.Second)
			continue
		}

		req.Header.Set("Authorization", "Bearer "+cfg.Token)
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			log.Printf("heartbeat failed: %v", err)
			time.Sleep(30 * time.Second)
			continue
		}

		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			log.Printf("heartbeat ok: %s", resp.Status)
		} else {
			log.Printf("heartbeat rejected: %s body=%s", resp.Status, string(body))
		}

		time.Sleep(30 * time.Second)
	}
}
