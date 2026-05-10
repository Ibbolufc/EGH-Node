package main

import (
    "bytes"
    "crypto/rand"
    "encoding/hex"
    "encoding/json"
    "errors"
    "fmt"
    "io"
    "log"
    "net/http"
    "os"
    "path/filepath"
    "strings"
    "time"

    jwt "github.com/golang-jwt/jwt/v5"
)

type daemonServerRecord struct {
    UUID           string            `json:"uuid"`
    Invocation     string            `json:"invocation"`
    Environment    map[string]string `json:"environment"`
    Image          string            `json:"image"`
    MemoryLimit    int               `json:"memory_limit"`
    DiskLimit      int               `json:"disk_limit"`
    CPULimit       int               `json:"cpu_limit"`
    AllocationIP   string            `json:"allocation_ip"`
    AllocationPort int               `json:"allocation_port"`
    State          string            `json:"state"`
    CreatedAt      time.Time         `json:"created_at"`
    UpdatedAt      time.Time         `json:"updated_at"`
}

type provisionRequest struct {
    UUID     string `json:"uuid"`
    Settings struct {
        Environment map[string]string `json:"environment"`
        Invocation  string            `json:"invocation"`
        Build       struct {
            MemoryLimit int `json:"memory_limit"`
            Disk        int `json:"disk"`
            CPULimit    int `json:"cpu_limit"`
        } `json:"build"`
        Container struct {
            Image string `json:"image"`
            Env   map[string]string `json:"env"`
        } `json:"container"`
        Allocations struct {
            Default struct {
                IP   string `json:"ip"`
                Port int    `json:"port"`
            } `json:"default"`
        } `json:"allocations"`
    } `json:"settings"`
}

type powerRequest struct {
    Action string `json:"action"`
}

type resourceResponse struct {
    CurrentState string `json:"current_state"`
    Resources    struct {
        CPUAbsolute    float64 `json:"cpu_absolute"`
        MemoryBytes    int64   `json:"memory_bytes"`
        DiskBytes      int64   `json:"disk_bytes"`
        NetworkRxBytes int64   `json:"network_rx_bytes"`
        NetworkTxBytes int64   `json:"network_tx_bytes"`
        Uptime         int64   `json:"uptime"`
    } `json:"resources"`
}

type installCallbackBody struct {
    Successful bool `json:"successful"`
    Reinstall  bool `json:"reinstall"`
}

func registerServerRoutes(mux *http.ServeMux, cfg *Config) {
    _ = os.MkdirAll(filepath.Join(cfg.System.Data, "servers"), 0o755)
    mux.HandleFunc("/api/servers/", requireDaemonAuth(cfg, serverRouter(cfg)))
}

func serverRouter(cfg *Config) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        trimmed := strings.TrimPrefix(r.URL.Path, "/api/servers/")
        trimmed = strings.Trim(trimmed, "/")
        if trimmed == "" {
            http.NotFound(w, r)
            return
        }
        parts := strings.Split(trimmed, "/")
        uuid := parts[0]
        if uuid == "" {
            http.NotFound(w, r)
            return
        }

        switch {
        case len(parts) == 1 && r.Method == http.MethodPost:
            handleProvisionServer(cfg, uuid, w, r)
        case len(parts) == 1 && r.Method == http.MethodDelete:
            handleDeleteServer(cfg, uuid, w, r)
        case len(parts) == 2 && parts[1] == "install" && r.Method == http.MethodPost:
            handleInstallServer(cfg, uuid, w, r)
        case len(parts) == 2 && parts[1] == "power" && r.Method == http.MethodPost:
            handlePowerAction(cfg, uuid, w, r)
        case len(parts) == 2 && parts[1] == "resources" && r.Method == http.MethodGet:
            handleGetResources(cfg, uuid, w, r)
        default:
            http.NotFound(w, r)
        }
    }
}

func handleProvisionServer(cfg *Config, uuid string, w http.ResponseWriter, r *http.Request) {
    var req provisionRequest
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        http.Error(w, fmt.Sprintf("invalid provision payload: %v", err), http.StatusBadRequest)
        return
    }
    if req.UUID != "" {
        uuid = req.UUID
    }

    now := time.Now().UTC()
    record := &daemonServerRecord{
        UUID:           uuid,
        Invocation:     req.Settings.Invocation,
        Environment:    coalesceEnv(req.Settings.Environment, req.Settings.Container.Env),
        Image:          req.Settings.Container.Image,
        MemoryLimit:    req.Settings.Build.MemoryLimit,
        DiskLimit:      req.Settings.Build.Disk,
        CPULimit:       req.Settings.Build.CPULimit,
        AllocationIP:   req.Settings.Allocations.Default.IP,
        AllocationPort: req.Settings.Allocations.Default.Port,
        State:          "installing",
        CreatedAt:      now,
        UpdatedAt:      now,
    }

    if err := saveServerRecord(cfg, record); err != nil {
        http.Error(w, fmt.Sprintf("failed to persist server: %v", err), http.StatusInternalServerError)
        return
    }

    w.WriteHeader(http.StatusNoContent)
}

func handleInstallServer(cfg *Config, uuid string, w http.ResponseWriter, r *http.Request) {
    record, err := loadServerRecord(cfg, uuid)
    if err != nil {
        if errors.Is(err, os.ErrNotExist) {
            http.Error(w, "server not found", http.StatusNotFound)
            return
        }
        http.Error(w, fmt.Sprintf("failed to load server: %v", err), http.StatusInternalServerError)
        return
    }

    record.State = "installing"
    record.UpdatedAt = time.Now().UTC()
    if err := saveServerRecord(cfg, record); err != nil {
        http.Error(w, fmt.Sprintf("failed to persist install state: %v", err), http.StatusInternalServerError)
        return
    }

    go completeInstallAsync(cfg, uuid)
    w.WriteHeader(http.StatusNoContent)
}

func completeInstallAsync(cfg *Config, uuid string) {
    time.Sleep(1500 * time.Millisecond)

    record, err := loadServerRecord(cfg, uuid)
    if err != nil {
        log.Printf("install callback skipped for %s: %v", uuid, err)
        return
    }

    record.State = "offline"
    record.UpdatedAt = time.Now().UTC()
    if err := saveServerRecord(cfg, record); err != nil {
        log.Printf("install state update failed for %s: %v", uuid, err)
        return
    }

    if err := sendInstallCallback(cfg, uuid, true); err != nil {
        log.Printf("install callback failed for %s: %v", uuid, err)
    } else {
        log.Printf("install completed for %s", uuid)
    }
}

func handlePowerAction(cfg *Config, uuid string, w http.ResponseWriter, r *http.Request) {
    record, err := loadServerRecord(cfg, uuid)
    if err != nil {
        if errors.Is(err, os.ErrNotExist) {
            http.Error(w, "server not found", http.StatusNotFound)
            return
        }
        http.Error(w, fmt.Sprintf("failed to load server: %v", err), http.StatusInternalServerError)
        return
    }

    var req powerRequest
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        http.Error(w, fmt.Sprintf("invalid power payload: %v", err), http.StatusBadRequest)
        return
    }

    switch req.Action {
    case "start", "restart":
        record.State = "running"
    case "stop", "kill":
        record.State = "offline"
    default:
        http.Error(w, "unsupported power action", http.StatusBadRequest)
        return
    }
    record.UpdatedAt = time.Now().UTC()

    if err := saveServerRecord(cfg, record); err != nil {
        http.Error(w, fmt.Sprintf("failed to persist power state: %v", err), http.StatusInternalServerError)
        return
    }

    w.WriteHeader(http.StatusNoContent)
}

func handleGetResources(cfg *Config, uuid string, w http.ResponseWriter, r *http.Request) {
    record, err := loadServerRecord(cfg, uuid)
    if err != nil {
        if errors.Is(err, os.ErrNotExist) {
            http.Error(w, "server not found", http.StatusNotFound)
            return
        }
        http.Error(w, fmt.Sprintf("failed to load server: %v", err), http.StatusInternalServerError)
        return
    }

    var resp resourceResponse
    resp.CurrentState = record.State
    resp.Resources.CPUAbsolute = 0
    resp.Resources.MemoryBytes = 0
    resp.Resources.DiskBytes = 0
    resp.Resources.NetworkRxBytes = 0
    resp.Resources.NetworkTxBytes = 0
    resp.Resources.Uptime = 0

    w.Header().Set("Content-Type", "application/json")
    _ = json.NewEncoder(w).Encode(resp)
}

func handleDeleteServer(cfg *Config, uuid string, w http.ResponseWriter, r *http.Request) {
    path := serverRecordPath(cfg, uuid)
    if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
        http.Error(w, fmt.Sprintf("failed to delete server: %v", err), http.StatusInternalServerError)
        return
    }
    _ = os.Remove(serverRecordDir(cfg, uuid))
    w.WriteHeader(http.StatusNoContent)
}

func sendInstallCallback(cfg *Config, uuid string, successful bool) error {
    callbackURL := fmt.Sprintf("%s/api/remote/servers/%s/install", strings.TrimRight(cfg.Remote, "/"), uuid)
    body := installCallbackBody{Successful: successful, Reinstall: false}
    payload, err := json.Marshal(body)
    if err != nil {
        return fmt.Errorf("marshal callback body: %w", err)
    }

    token, err := signDaemonJWT(cfg.CurrentDaemonSecret())
    if err != nil {
        return fmt.Errorf("sign callback token: %w", err)
    }

    req, err := http.NewRequest(http.MethodPost, callbackURL, bytes.NewReader(payload))
    if err != nil {
        return fmt.Errorf("build callback request: %w", err)
    }
    req.Header.Set("Authorization", "Bearer "+token)
    req.Header.Set("Content-Type", "application/json")

    client := &http.Client{Timeout: 10 * time.Second}
    resp, err := client.Do(req)
    if err != nil {
        return fmt.Errorf("send callback: %w", err)
    }
    defer resp.Body.Close()

    if resp.StatusCode < 200 || resp.StatusCode >= 300 {
        bodyBytes, _ := io.ReadAll(resp.Body)
        return fmt.Errorf("callback rejected: %s body=%s", resp.Status, string(bodyBytes))
    }
    return nil
}

func signDaemonJWT(secret string) (string, error) {
    if secret == "" {
        return "", errors.New("missing daemon secret")
    }
    now := time.Now().Unix()
    claims := jwt.MapClaims{
        "jti": randomJTI(),
        "iat": now,
        "nbf": now - 5,
        "exp": now + 300,
    }
    token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
    return token.SignedString([]byte(secret))
}

func randomJTI() string {
    b := make([]byte, 16)
    if _, err := rand.Read(b); err != nil {
        return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
    }
    return hex.EncodeToString(b)
}

func saveServerRecord(cfg *Config, record *daemonServerRecord) error {
    dir := serverRecordDir(cfg, record.UUID)
    if err := os.MkdirAll(dir, 0o755); err != nil {
        return err
    }
    data, err := json.MarshalIndent(record, "", "  ")
    if err != nil {
        return err
    }
    return os.WriteFile(serverRecordPath(cfg, record.UUID), data, 0o644)
}

func loadServerRecord(cfg *Config, uuid string) (*daemonServerRecord, error) {
    data, err := os.ReadFile(serverRecordPath(cfg, uuid))
    if err != nil {
        return nil, err
    }
    var record daemonServerRecord
    if err := json.Unmarshal(data, &record); err != nil {
        return nil, err
    }
    return &record, nil
}

func serverRecordDir(cfg *Config, uuid string) string {
    return filepath.Join(cfg.System.Data, "servers", uuid)
}

func serverRecordPath(cfg *Config, uuid string) string {
    return filepath.Join(serverRecordDir(cfg, uuid), "server.json")
}

func coalesceEnv(primary map[string]string, fallback map[string]string) map[string]string {
    if len(primary) > 0 {
        return primary
    }
    if len(fallback) > 0 {
        return fallback
    }
    return map[string]string{}
}
