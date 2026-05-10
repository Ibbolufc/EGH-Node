package main

import (
    "bytes"
    "crypto/rand"
    "encoding/hex"
    "encoding/json"
    "errors"
    "fmt"
    "io"
    "io/fs"
    "log"
    "mime"
    "net/http"
    "os"
    "path/filepath"
    "strings"
    "time"

    jwt "github.com/golang-jwt/jwt/v5"
    "github.com/gorilla/websocket"
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

type daemonFileEntry struct {
    Name       string `json:"name"`
    Size       int64  `json:"size"`
    IsFile     bool   `json:"is_file"`
    IsSymlink  bool   `json:"is_symlink"`
    IsEditable bool   `json:"is_editable"`
    MimeType   string `json:"mime_type"`
    CreatedAt  string `json:"created_at"`
    ModifiedAt string `json:"modified_at"`
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
            Image string            `json:"image"`
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

type commandRequest struct {
    Command string `json:"command"`
}

type deleteFilesRequest struct {
    Root  string   `json:"root"`
    Files []string `json:"files"`
}

type renameFilesRequest struct {
    Root  string `json:"root"`
    Files []struct {
        From string `json:"from"`
        To   string `json:"to"`
    } `json:"files"`
}

type createDirectoryRequest struct {
    Root string `json:"root"`
    Name string `json:"name"`
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

type daemonWsEnvelope struct {
    Event string   `json:"event"`
    Args  []string `json:"args"`
}

var daemonUpgrader = websocket.Upgrader{
    CheckOrigin: func(r *http.Request) bool { return true },
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
        case len(parts) == 2 && parts[1] == "commands" && r.Method == http.MethodPost:
            handleSendCommand(cfg, uuid, w, r)
        case len(parts) == 2 && parts[1] == "ws" && r.Method == http.MethodGet:
            handleServerWebSocket(cfg, uuid, w, r)
        case len(parts) == 3 && parts[1] == "files" && parts[2] == "list" && r.Method == http.MethodGet:
            handleListFiles(cfg, uuid, w, r)
        case len(parts) == 3 && parts[1] == "files" && parts[2] == "contents" && r.Method == http.MethodGet:
            handleReadFile(cfg, uuid, w, r)
        case len(parts) == 3 && parts[1] == "files" && parts[2] == "write" && r.Method == http.MethodPost:
            handleWriteFile(cfg, uuid, w, r)
        case len(parts) == 3 && parts[1] == "files" && parts[2] == "delete" && r.Method == http.MethodPost:
            handleDeleteFiles(cfg, uuid, w, r)
        case len(parts) == 3 && parts[1] == "files" && parts[2] == "rename" && r.Method == http.MethodPut:
            handleRenameFiles(cfg, uuid, w, r)
        case len(parts) == 3 && parts[1] == "files" && parts[2] == "create-directory" && r.Method == http.MethodPost:
            handleCreateDirectory(cfg, uuid, w, r)
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
    if err := os.MkdirAll(serverFSRoot(cfg, uuid), 0o755); err != nil {
        http.Error(w, fmt.Sprintf("failed to create server filesystem: %v", err), http.StatusInternalServerError)
        return
    }

    log.Printf("server provisioned uuid=%s image=%s memory=%dMB disk=%dMB cpu=%d%% alloc=%s:%d", record.UUID, record.Image, record.MemoryLimit, record.DiskLimit, record.CPULimit, record.AllocationIP, record.AllocationPort)
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

    log.Printf("server install requested uuid=%s", uuid)
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

    log.Printf("server power action uuid=%s action=%s state=%s", uuid, req.Action, record.State)
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
    resp.Resources.DiskBytes = directorySize(serverFSRoot(cfg, uuid))
    resp.Resources.NetworkRxBytes = 0
    resp.Resources.NetworkTxBytes = 0
    resp.Resources.Uptime = 0

    log.Printf("server resources requested uuid=%s state=%s", uuid, record.State)
    w.Header().Set("Content-Type", "application/json")
    _ = json.NewEncoder(w).Encode(resp)
}

func handleSendCommand(cfg *Config, uuid string, w http.ResponseWriter, r *http.Request) {
    record, err := loadServerRecord(cfg, uuid)
    if err != nil {
        if errors.Is(err, os.ErrNotExist) {
            http.Error(w, "server not found", http.StatusNotFound)
            return
        }
        http.Error(w, fmt.Sprintf("failed to load server: %v", err), http.StatusInternalServerError)
        return
    }

    var req commandRequest
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        http.Error(w, fmt.Sprintf("invalid command payload: %v", err), http.StatusBadRequest)
        return
    }

    log.Printf("server command received uuid=%s state=%s command=%q", uuid, record.State, req.Command)
    w.WriteHeader(http.StatusNoContent)
}

func handleListFiles(cfg *Config, uuid string, w http.ResponseWriter, r *http.Request) {
    dir := r.URL.Query().Get("directory")
    resolved, err := resolveServerPath(cfg, uuid, dir)
    if err != nil {
        http.Error(w, err.Error(), http.StatusBadRequest)
        return
    }

    entries, err := os.ReadDir(resolved)
    if err != nil {
        http.Error(w, fmt.Sprintf("failed to read directory: %v", err), http.StatusInternalServerError)
        return
    }

    files := make([]daemonFileEntry, 0, len(entries))
    for _, entry := range entries {
        info, statErr := entry.Info()
        if statErr != nil {
            continue
        }
        mimeType := "inode/directory"
        if !entry.IsDir() {
            ext := filepath.Ext(entry.Name())
            mimeType = mime.TypeByExtension(ext)
            if mimeType == "" {
                mimeType = "text/plain"
            }
        }
        files = append(files, daemonFileEntry{
            Name:       entry.Name(),
            Size:       info.Size(),
            IsFile:     !entry.IsDir(),
            IsSymlink:  info.Mode()&os.ModeSymlink != 0,
            IsEditable: !entry.IsDir(),
            MimeType:   mimeType,
            CreatedAt:  info.ModTime().UTC().Format(time.RFC3339),
            ModifiedAt: info.ModTime().UTC().Format(time.RFC3339),
        })
    }

    w.Header().Set("Content-Type", "application/json")
    _ = json.NewEncoder(w).Encode(map[string]any{"files": files})
}

func handleReadFile(cfg *Config, uuid string, w http.ResponseWriter, r *http.Request) {
    fileParam := r.URL.Query().Get("file")
    resolved, err := resolveServerPath(cfg, uuid, fileParam)
    if err != nil {
        http.Error(w, err.Error(), http.StatusBadRequest)
        return
    }

    data, err := os.ReadFile(resolved)
    if err != nil {
        http.Error(w, fmt.Sprintf("failed to read file: %v", err), http.StatusInternalServerError)
        return
    }

    w.Header().Set("Content-Type", "text/plain; charset=utf-8")
    _, _ = w.Write(data)
}

func handleWriteFile(cfg *Config, uuid string, w http.ResponseWriter, r *http.Request) {
    fileParam := r.URL.Query().Get("file")
    resolved, err := resolveServerPath(cfg, uuid, fileParam)
    if err != nil {
        http.Error(w, err.Error(), http.StatusBadRequest)
        return
    }

    if err := os.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
        http.Error(w, fmt.Sprintf("failed to create parent directory: %v", err), http.StatusInternalServerError)
        return
    }
    body, err := io.ReadAll(r.Body)
    if err != nil {
        http.Error(w, fmt.Sprintf("failed to read request body: %v", err), http.StatusBadRequest)
        return
    }
    if err := os.WriteFile(resolved, body, 0o644); err != nil {
        http.Error(w, fmt.Sprintf("failed to write file: %v", err), http.StatusInternalServerError)
        return
    }
    log.Printf("server file written uuid=%s path=%s", uuid, fileParam)
    w.WriteHeader(http.StatusNoContent)
}

func handleDeleteFiles(cfg *Config, uuid string, w http.ResponseWriter, r *http.Request) {
    var req deleteFilesRequest
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        http.Error(w, fmt.Sprintf("invalid delete payload: %v", err), http.StatusBadRequest)
        return
    }
    for _, file := range req.Files {
        combined := filepath.ToSlash(filepath.Join(req.Root, file))
        resolved, err := resolveServerPath(cfg, uuid, combined)
        if err != nil {
            http.Error(w, err.Error(), http.StatusBadRequest)
            return
        }
        if err := os.RemoveAll(resolved); err != nil {
            http.Error(w, fmt.Sprintf("failed to delete %s: %v", file, err), http.StatusInternalServerError)
            return
        }
    }
    log.Printf("server files deleted uuid=%s count=%d", uuid, len(req.Files))
    w.WriteHeader(http.StatusNoContent)
}

func handleRenameFiles(cfg *Config, uuid string, w http.ResponseWriter, r *http.Request) {
    var req renameFilesRequest
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        http.Error(w, fmt.Sprintf("invalid rename payload: %v", err), http.StatusBadRequest)
        return
    }
    for _, file := range req.Files {
        fromResolved, err := resolveServerPath(cfg, uuid, filepath.ToSlash(filepath.Join(req.Root, file.From)))
        if err != nil {
            http.Error(w, err.Error(), http.StatusBadRequest)
            return
        }
        toResolved, err := resolveServerPath(cfg, uuid, filepath.ToSlash(filepath.Join(req.Root, file.To)))
        if err != nil {
            http.Error(w, err.Error(), http.StatusBadRequest)
            return
        }
        if err := os.MkdirAll(filepath.Dir(toResolved), 0o755); err != nil {
            http.Error(w, fmt.Sprintf("failed to create target directory: %v", err), http.StatusInternalServerError)
            return
        }
        if err := os.Rename(fromResolved, toResolved); err != nil {
            http.Error(w, fmt.Sprintf("failed to rename file: %v", err), http.StatusInternalServerError)
            return
        }
    }
    log.Printf("server files renamed uuid=%s count=%d", uuid, len(req.Files))
    w.WriteHeader(http.StatusNoContent)
}

func handleCreateDirectory(cfg *Config, uuid string, w http.ResponseWriter, r *http.Request) {
    var req createDirectoryRequest
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        http.Error(w, fmt.Sprintf("invalid create-directory payload: %v", err), http.StatusBadRequest)
        return
    }
    target := filepath.ToSlash(filepath.Join(req.Root, req.Name))
    resolved, err := resolveServerPath(cfg, uuid, target)
    if err != nil {
        http.Error(w, err.Error(), http.StatusBadRequest)
        return
    }
    if err := os.MkdirAll(resolved, 0o755); err != nil {
        http.Error(w, fmt.Sprintf("failed to create directory: %v", err), http.StatusInternalServerError)
        return
    }
    log.Printf("server directory created uuid=%s path=%s", uuid, target)
    w.WriteHeader(http.StatusNoContent)
}

func handleServerWebSocket(cfg *Config, uuid string, w http.ResponseWriter, r *http.Request) {
    record, err := loadServerRecord(cfg, uuid)
    if err != nil {
        if errors.Is(err, os.ErrNotExist) {
            http.Error(w, "server not found", http.StatusNotFound)
            return
        }
        http.Error(w, fmt.Sprintf("failed to load server: %v", err), http.StatusInternalServerError)
        return
    }

    conn, err := daemonUpgrader.Upgrade(w, r, nil)
    if err != nil {
        log.Printf("server ws upgrade failed uuid=%s: %v", uuid, err)
        return
    }
    defer conn.Close()

    log.Printf("server websocket connected uuid=%s", uuid)
    _ = conn.WriteJSON(daemonWsEnvelope{Event: "auth success", Args: []string{}})
    _ = conn.WriteJSON(daemonWsEnvelope{Event: "status", Args: []string{record.State}})
    _ = conn.WriteJSON(daemonWsEnvelope{Event: "console output", Args: []string{fmt.Sprintf("[EGH Node] Connected to server %s", uuid)}})

    ticker := time.NewTicker(5 * time.Second)
    defer ticker.Stop()

    done := make(chan struct{})

    go func() {
        defer close(done)
        for {
            var msg daemonWsEnvelope
            if err := conn.ReadJSON(&msg); err != nil {
                if !websocket.IsCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
                    log.Printf("server websocket read failed uuid=%s: %v", uuid, err)
                }
                return
            }

            current, loadErr := loadServerRecord(cfg, uuid)
            if loadErr != nil {
                log.Printf("server websocket load failed uuid=%s: %v", uuid, loadErr)
                return
            }

            switch msg.Event {
            case "send command":
                command := ""
                if len(msg.Args) > 0 {
                    command = msg.Args[0]
                }
                log.Printf("server websocket command uuid=%s command=%q", uuid, command)
                _ = conn.WriteJSON(daemonWsEnvelope{Event: "console output", Args: []string{fmt.Sprintf("> %s", command)}})
            case "set state":
                if len(msg.Args) == 0 {
                    continue
                }
                action := msg.Args[0]
                switch action {
                case "start", "restart":
                    current.State = "running"
                case "stop", "kill":
                    current.State = "offline"
                default:
                    _ = conn.WriteJSON(daemonWsEnvelope{Event: "console output", Args: []string{fmt.Sprintf("[EGH Node] Unknown state action: %s", action)}})
                    continue
                }
                current.UpdatedAt = time.Now().UTC()
                if err := saveServerRecord(cfg, current); err != nil {
                    log.Printf("server websocket save failed uuid=%s: %v", uuid, err)
                    continue
                }
                log.Printf("server websocket state uuid=%s action=%s state=%s", uuid, action, current.State)
                _ = conn.WriteJSON(daemonWsEnvelope{Event: "status", Args: []string{current.State}})
                _ = conn.WriteJSON(daemonWsEnvelope{Event: "console output", Args: []string{fmt.Sprintf("[EGH Node] State changed to %s", current.State)}})
            }
        }
    }()

    for {
        select {
        case <-done:
            log.Printf("server websocket disconnected uuid=%s", uuid)
            return
        case <-ticker.C:
            current, loadErr := loadServerRecord(cfg, uuid)
            if loadErr != nil {
                log.Printf("server websocket ticker load failed uuid=%s: %v", uuid, loadErr)
                return
            }
            payload, _ := json.Marshal(map[string]any{
                "cpu_absolute":       0,
                "memory_bytes":       0,
                "memory_limit_bytes": int64(current.MemoryLimit) * 1024 * 1024,
                "disk_bytes":         directorySize(serverFSRoot(cfg, uuid)),
                "network": map[string]any{
                    "rx_bytes": 0,
                    "tx_bytes": 0,
                },
                "uptime": 0,
                "state":  current.State,
            })
            if err := conn.WriteJSON(daemonWsEnvelope{Event: "stats", Args: []string{string(payload)}}); err != nil {
                log.Printf("server websocket stats write failed uuid=%s: %v", uuid, err)
                return
            }
            if err := conn.WriteJSON(daemonWsEnvelope{Event: "status", Args: []string{current.State}}); err != nil {
                log.Printf("server websocket status write failed uuid=%s: %v", uuid, err)
                return
            }
        }
    }
}

func handleDeleteServer(cfg *Config, uuid string, w http.ResponseWriter, r *http.Request) {
    path := serverRecordPath(cfg, uuid)
    if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
        http.Error(w, fmt.Sprintf("failed to delete server: %v", err), http.StatusInternalServerError)
        return
    }
    _ = os.RemoveAll(serverFSRoot(cfg, uuid))
    _ = os.Remove(serverRecordDir(cfg, uuid))
    log.Printf("server deleted uuid=%s", uuid)
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

func serverFSRoot(cfg *Config, uuid string) string {
    return filepath.Join(serverRecordDir(cfg, uuid), "fs")
}

func resolveServerPath(cfg *Config, uuid string, unsafePath string) (string, error) {
    root := serverFSRoot(cfg, uuid)
    if err := os.MkdirAll(root, 0o755); err != nil {
        return "", fmt.Errorf("failed to prepare server filesystem: %w", err)
    }
    cleaned := strings.TrimSpace(unsafePath)
    if cleaned == "" || cleaned == "/" {
        return root, nil
    }
    cleaned = filepath.ToSlash(filepath.Clean("/" + cleaned))
    rel := strings.TrimPrefix(cleaned, "/")
    resolved := filepath.Join(root, rel)
    relative, err := filepath.Rel(root, resolved)
    if err != nil {
        return "", fmt.Errorf("invalid path")
    }
    if relative == ".." || strings.HasPrefix(relative, fmt.Sprintf("..%c", os.PathSeparator)) {
        return "", fmt.Errorf("path escapes server filesystem")
    }
    return resolved, nil
}

func directorySize(root string) int64 {
    var total int64
    _ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
        if err != nil || d == nil || d.IsDir() {
            return nil
        }
        info, statErr := d.Info()
        if statErr == nil {
            total += info.Size()
        }
        return nil
    })
    return total
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
