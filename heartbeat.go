package main

import (
    "bytes"
    "encoding/json"
    "fmt"
    "io"
    "log"
    "net/http"
    "strings"
    "time"
)

type heartbeatResponse struct {
    OK          bool   `json:"ok"`
    Status      string `json:"status"`
    DaemonToken string `json:"daemonToken"`
}

func heartbeatLoop(cfg *Config, configPath string, nodeID string) {
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
            var parsed heartbeatResponse
            if len(body) > 0 {
                if err := json.Unmarshal(body, &parsed); err != nil {
                    log.Printf("heartbeat ok: %s (could not parse response body: %v)", resp.Status, err)
                } else if cfg.UpdateDaemonToken(parsed.DaemonToken) {
                    if err := SaveConfig(configPath, cfg); err != nil {
                        log.Printf("heartbeat ok: %s (daemon token updated but config save failed: %v)", resp.Status, err)
                    } else {
                        log.Printf("heartbeat ok: %s (daemon token updated)", resp.Status)
                    }
                } else {
                    log.Printf("heartbeat ok: %s", resp.Status)
                }
            } else {
                log.Printf("heartbeat ok: %s", resp.Status)
            }
        } else {
            log.Printf("heartbeat rejected: %s body=%s", resp.Status, string(body))
        }

        time.Sleep(30 * time.Second)
    }
}
